package xray

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

const accessLogFixture = "testdata/access_log_real.txt"

// Needs XRAY_E2E_BINARY (the Xray the panel ships, scripts/xray-build). XRAY_E2E_WRITE_FIXTURE=1
// rewrites the lines TestParseAccessLogLineOnRealLines reads; its ports then need updating.
func TestAccessLog_E2E(t *testing.T) {
	bin := os.Getenv("XRAY_E2E_BINARY")
	if bin == "" {
		t.Skip("set XRAY_E2E_BINARY to an xray binary to run this test")
	}

	echo4 := listenEcho(t, "127.0.0.1:0")
	echoPort := echo4.Addr().(*net.TCPAddr).Port
	listenEcho(t, fmt.Sprintf("[::1]:%d", echoPort))
	udpEcho := listenUDPEcho(t)
	web := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })}
	webListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = web.Serve(webListener) }()
	t.Cleanup(func() { _ = web.Close() })
	webPort := webListener.Addr().(*net.TCPAddr).Port

	const goodID, badID = "a17e367c-2074-4d3e-aaeb-fbef5dfde7e7", "b28f478d-3185-4e4f-bbfc-0cf06e0ef8f8"
	socks, socks6, httpIn, vless, chain, badChain := freePort(t), freePort(t), freePort(t), freePort(t), freePort(t), freePort(t)
	socksIn := func(tag, listen string, port int) map[string]any {
		return map[string]any{
			"tag": tag, "listen": listen, "port": port, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": true},
		}
	}
	vlessOut := func(tag, id string) map[string]any {
		return map[string]any{"tag": tag, "protocol": "vless", "settings": map[string]any{
			"vnext": []any{map[string]any{"address": "127.0.0.1", "port": vless, "users": []any{map[string]any{"id": id, "encryption": "none"}}}},
		}}
	}
	dir := t.TempDir()
	accessLog := filepath.Join(dir, "access.log")
	cfg := map[string]any{
		"log": map[string]any{"access": accessLog, "loglevel": "warning"},
		"inbounds": []any{
			socksIn("in-socks", "127.0.0.1", socks),
			socksIn("in-socks6", "::1", socks6),
			socksIn("in-chain", "127.0.0.1", chain),
			socksIn("in-badchain", "127.0.0.1", badChain),
			map[string]any{"tag": "in-http", "listen": "127.0.0.1", "port": httpIn, "protocol": "http", "settings": map[string]any{}},
			map[string]any{"tag": "in-vless", "listen": "127.0.0.1", "port": vless, "protocol": "vless", "settings": map[string]any{
				"clients":    []any{map[string]any{"id": goodID, "email": "alice@example.com"}},
				"decryption": "none",
			}},
		},
		"outbounds": []any{
			map[string]any{"tag": "direct", "protocol": "freedom"},
			map[string]any{"tag": "blocked", "protocol": "blackhole"},
			vlessOut("to-vless", goodID),
			vlessOut("to-vless-bad", badID),
		},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"in-chain"}, "outboundTag": "to-vless"},
			map[string]any{"type": "field", "inboundTag": []string{"in-badchain"}, "outboundTag": "to-vless-bad"},
			map[string]any{"type": "field", "domain": []string{"full:blocked.test"}, "outboundTag": "blocked"},
		}},
	}
	cfgBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, cfgBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-c", cfgPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start xray: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	for _, port := range []int{socks, httpIn, vless, chain, badChain} {
		waitForPort(t, port)
	}

	echoAddr4 := fmt.Sprintf("127.0.0.1:%d", echoPort)
	through := func(proxyAddr, target string) {
		t.Helper()
		dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := dialer.Dial("tcp", target)
		if err != nil {
			t.Fatalf("dial %s through %s: %v", target, proxyAddr, err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write([]byte("ping"))
		_, _ = io.ReadFull(conn, make([]byte, 4))
	}
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socks)
	through(socksAddr, echoAddr4)
	through(socksAddr, fmt.Sprintf("localhost:%d", echoPort))
	through(socksAddr, fmt.Sprintf("[::1]:%d", echoPort))
	through(fmt.Sprintf("[::1]:%d", socks6), echoAddr4)
	through(socksAddr, "blocked.test:80")
	through(fmt.Sprintf("127.0.0.1:%d", chain), echoAddr4)
	through(fmt.Sprintf("127.0.0.1:%d", badChain), echoAddr4)
	socksUDPPing(t, socksAddr, udpEcho)
	socksConnectDomain(t, socksAddr, "evil [in-x -> out-x] email: victim", 443)

	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", httpIn))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/some/path?q=1", webPort))
	if err != nil {
		t.Fatalf("GET through the http inbound: %v", err)
	}
	_ = resp.Body.Close()

	want := []AccessLogLine{
		{Accepted: true, To: "tcp:" + echoAddr4, Inbound: "in-socks", Outbound: "direct"},
		{Accepted: true, To: fmt.Sprintf("tcp:localhost:%d", echoPort), Inbound: "in-socks", Outbound: "direct"},
		{Accepted: true, To: fmt.Sprintf("tcp:[::1]:%d", echoPort), Inbound: "in-socks", Outbound: "direct"},
		{Accepted: true, To: "tcp:" + echoAddr4, Inbound: "in-socks6", Outbound: "direct"},
		{Accepted: true, To: "tcp:blocked.test:80", Inbound: "in-socks", Outbound: "blocked"},
		{Accepted: true, To: "tcp:" + echoAddr4, Inbound: "in-chain", Outbound: "to-vless"},
		{Accepted: true, To: "tcp:" + echoAddr4, Inbound: "in-badchain", Outbound: "to-vless-bad"},
		{Accepted: true, To: "tcp:" + echoAddr4, Inbound: "in-vless", Outbound: "direct", Email: "alice@example.com"},
		{Accepted: true, To: "udp:" + udpEcho, Inbound: "in-socks", Outbound: "direct"},
		{Accepted: true, To: fmt.Sprintf("http://127.0.0.1:%d/some/path?q=1", webPort), Inbound: "in-http", Outbound: "direct"},
	}
	var lines []string
	var parsed []AccessLogLine
	missing := func() []AccessLogLine {
		raw, _ := os.ReadFile(accessLog)
		lines = strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		parsed = parsed[:0]
		for _, line := range lines {
			parsed = append(parsed, ParseAccessLogLine(line))
		}
		var absent []AccessLogLine
		for _, w := range want {
			found := false
			for _, got := range parsed {
				got.Time, got.From = time.Time{}, ""
				found = found || got == w
			}
			if !found {
				absent = append(absent, w)
			}
		}
		return absent
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(missing()) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if absent := missing(); len(absent) > 0 {
		t.Fatalf("no access-log line parsed as %+v\nlog:\n%s", absent, strings.Join(lines, "\n"))
	}

	var rejected, fromV6 bool
	for i, got := range parsed {
		if got.Time.IsZero() || time.Since(got.Time) > time.Hour || time.Until(got.Time) > time.Minute {
			t.Errorf("line %q: time parsed as %v", lines[i], got.Time)
		}
		// the socks inbound names the network before the address, the others do not
		addr, err := netip.ParseAddrPort(strings.TrimPrefix(strings.TrimPrefix(got.From, "tcp:"), "udp:"))
		if err != nil || !addr.Addr().IsLoopback() {
			t.Errorf("line %q: source %q is not a loopback address with a port", lines[i], got.From)
		}
		fromV6 = fromV6 || (err == nil && addr.Addr().Is6() && got.Inbound == "in-socks6")
		if !got.Accepted {
			rejected = true
			if got.To != "" || got.Inbound != "" || got.Outbound != "" || got.Email != "" {
				t.Errorf("refused line %q parsed as a connection: %+v", lines[i], got)
			}
		}
	}
	for i, got := range parsed {
		if strings.Contains(got.Email, "victim") || got.Inbound == "in-x" || got.Outbound == "out-x" {
			t.Errorf("a destination imitating a route and an email was believed: %q parsed as %+v", lines[i], got)
		}
	}
	if !rejected {
		t.Errorf("the wrong VLESS id left no refused line:\n%s", strings.Join(lines, "\n"))
	}
	if !fromV6 {
		t.Errorf("no line from an IPv6 source through in-socks6:\n%s", strings.Join(lines, "\n"))
	}

	if os.Getenv("XRAY_E2E_WRITE_FIXTURE") == "1" {
		if err := os.WriteFile(accessLogFixture, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func listenEcho(t *testing.T, addr string) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return l
}

func listenUDPEcho(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteTo(buf[:n], from)
		}
	}()
	return conn.LocalAddr().String()
}

// socksConnectDomain asks for a TCP connection to a name no SOCKS library would send.
func socksConnectDomain(t *testing.T, proxyAddr, domain string, port int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 2)); err != nil {
		t.Fatalf("socks greeting: %v", err)
	}
	request := append([]byte{5, 1, 0, 3, byte(len(domain))}, domain...)
	if _, err := conn.Write(append(request, byte(port>>8), byte(port))); err != nil {
		t.Fatal(err)
	}
	// Xray answers or hangs up; either way the line, if any, is in the log by then.
	_, _ = io.ReadAll(conn)
}

// socksUDPPing sends one datagram to target through a SOCKS5 UDP association (RFC 1928 §7).
func socksUDPPing(t *testing.T, proxyAddr, target string) {
	t.Helper()
	ctrl, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	_ = ctrl.SetDeadline(time.Now().Add(5 * time.Second))
	reply := make([]byte, 10)
	if _, err := ctrl.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(ctrl, reply[:2]); err != nil {
		t.Fatalf("socks greeting: %v", err)
	}
	if _, err := ctrl.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(ctrl, reply); err != nil || reply[1] != 0 || reply[3] != 1 {
		t.Fatalf("socks udp associate: reply % x, err %v", reply, err)
	}
	relay := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(reply[8])<<8 | int(reply[9])}
	dst := netip.MustParseAddrPort(target)
	ip := dst.Addr().As4()
	packet := append([]byte{0, 0, 0, 1}, ip[:]...)
	packet = append(packet, byte(dst.Port()>>8), byte(dst.Port()), 'p', 'i', 'n', 'g')
	udp, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	_ = udp.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := udp.Write(packet); err != nil {
		t.Fatal(err)
	}
	if _, err := udp.Read(make([]byte, 64)); err != nil {
		t.Fatalf("no udp echo through the socks inbound: %v", err)
	}
}
