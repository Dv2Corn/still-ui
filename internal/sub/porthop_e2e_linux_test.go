//go:build linux

package sub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/porthop"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const (
	hopE2EPort  = 8443
	hopE2ERange = "21000-21999"
	hopE2ESubID = "hop-e2e-sub"
	// hopE2EGuard drops what a client sends straight to the inbound port, ahead of NAT:
	// whatever still arrives there came through a hop port and the redirect.
	hopE2EGuard = "table inet still_hop_e2e_guard {\n\tchain pre {\n\t\ttype filter hook prerouting priority raw; policy accept;\n\t\tudp dport 8443 drop\n\t}\n}\n"
)

func hopE2ENft(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("nft", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func hopE2ECertificate(t *testing.T) (certLines, keyLines []string, pin string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hop.test"},
		DNSNames:     []string{"hop.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	lines := func(blockType string, b []byte) []string {
		return strings.Split(strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: b}))), "\n")
	}
	sum := sha256.Sum256(der)
	return lines("CERTIFICATE", der), lines("EC PRIVATE KEY", keyDER), hex.EncodeToString(sum[:])
}

func hopE2EStartXray(t *testing.T, bin, dir, name string, config any) {
	t.Helper()
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(dir, name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run", "-c", path)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start xray: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = logFile.Close()
		if t.Failed() {
			out, _ := os.ReadFile(logFile.Name())
			t.Logf("%s xray log:\n%s", name, out)
		}
	})
}

// TestPortHopE2E drives the whole feature with real parts: the inbound is saved through the
// panel's own service, the redirect is loaded by nft, the server runs the inbound exactly as
// the panel hands it to Xray, and a second Xray in another network namespace connects with
// the JSON subscription the panel serves for it.
func TestPortHopE2E(t *testing.T) {
	shared, peer, self, bin := os.Getenv("XUI_PORTHOP_E2E_DIR"), os.Getenv("XUI_PORTHOP_E2E_PEER"), os.Getenv("XUI_PORTHOP_E2E_SELF"), os.Getenv("XRAY_E2E_BINARY")
	if shared == "" || peer == "" || self == "" || bin == "" {
		t.Skip("needs a second network namespace, nft with NET_ADMIN and an xray binary: run deploy/test/porthop-e2e.sh")
	}
	seedSubDB(t)

	if out, err := hopE2ENft(t, "add table inet still_hop_e2e_guard\ndelete table inet still_hop_e2e_guard\n"+hopE2EGuard, "-f", "-"); err != nil {
		t.Fatalf("load guard table: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_, _ = hopE2ENft(t, "", "delete", "table", "inet", "still_hop_e2e_guard")
		_, _ = hopE2ENft(t, "", "delete", "table", "inet", porthop.TableName)
	})

	cert, key, pin := hopE2ECertificate(t)
	stream := map[string]any{
		"network":          "hysteria",
		"security":         "tls",
		"hysteriaSettings": map[string]any{"version": 2, "auth": "", "udpIdleTimeout": 60},
		"tlsSettings": map[string]any{
			"serverName":   "hop.test",
			"alpn":         []string{"h3"},
			"certificates": []any{map[string]any{"certificate": cert, "key": key}},
			"settings":     map[string]any{"fingerprint": "", "pinnedPeerCertSha256": []string{pin}},
		},
		"finalmask": map[string]any{"quicParams": map[string]any{
			"congestion": "bbr",
			"udpHop":     map[string]any{"ports": hopE2ERange, "interval": "5-10", "redirect": true},
		}},
	}
	streamJSON, _ := json.Marshal(stream)
	inbounds := &service.InboundService{}
	inbound, _, err := inbounds.AddInbound(&model.Inbound{
		UserId: 1, Enable: true, Remark: "hop-e2e", Port: hopE2EPort, Protocol: model.Hysteria,
		Settings:       `{"version":2,"clients":[{"auth":"hop-e2e-auth","email":"hop@e2e","subId":"` + hopE2ESubID + `","enable":true}]}`,
		StreamSettings: string(streamJSON),
	})
	if err != nil {
		t.Fatalf("AddInbound: %v", err)
	}

	// The server: this inbound as the panel's config generator emits it, nothing else.
	panelConfig, err := (&service.XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	var serverInbound any
	for i := range panelConfig.InboundConfigs {
		if panelConfig.InboundConfigs[i].Tag == inbound.Tag {
			serverInbound = panelConfig.InboundConfigs[i]
		}
	}
	if serverInbound == nil {
		t.Fatalf("the panel config has no inbound tagged %q", inbound.Tag)
	}
	hopE2EStartXray(t, bin, shared, "server", map[string]any{
		"log":      map[string]any{"loglevel": "debug"},
		"inbounds": []any{serverInbound},
		// The target sits on the test network's private address, which freedom refuses by default.
		"outbounds": []any{map[string]any{
			"protocol": "freedom", "tag": "direct",
			"settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow"}}},
		}},
	})

	// The client: the JSON subscription, with only its local SOCKS listener opened to this test.
	body, _, err := NewSubJsonService("", "", "", "", NewSubService("")).GetJson(hopE2ESubID, self, false)
	if err != nil {
		t.Fatalf("GetJson: %v", err)
	}
	var client map[string]any
	if err := json.Unmarshal([]byte(body), &client); err != nil {
		t.Fatalf("subscription is not one JSON config: %v\n%s", err, body)
	}
	if !strings.Contains(body, `"remotePorts": "`+hopE2ERange+`"`) {
		t.Fatalf("the subscription does not carry the hop range as a udphop mask:\n%s", body)
	}
	for _, raw := range client["inbounds"].([]any) {
		raw.(map[string]any)["listen"] = "0.0.0.0"
	}
	client["log"] = map[string]any{"loglevel": "debug"}
	clientJSON, _ := json.MarshalIndent(client, "", "  ")
	// Renamed into place: the peer container starts its Xray as soon as the file appears.
	tmp := filepath.Join(shared, "client.json.tmp")
	if err := os.WriteFile(tmp, clientJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(shared, "client.json")); err != nil {
		t.Fatal(err)
	}

	target := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "hop-ok") })}
	ln, err := net.Listen("tcp", net.JoinHostPort(self, "0"))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = target.Serve(ln) }()
	t.Cleanup(func() { _ = target.Close() })

	socks := net.JoinHostPort(peer, "10808")
	waitUntil(t, 90*time.Second, "the peer's SOCKS listener", func() error {
		conn, err := net.DialTimeout("tcp", socks, 2*time.Second)
		if err == nil {
			_ = conn.Close()
		}
		return err
	})
	fetch := func() (string, error) {
		dialer, err := proxy.SOCKS5("tcp", socks, nil, &net.Dialer{Timeout: 5 * time.Second})
		if err != nil {
			return "", err
		}
		httpClient := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.(proxy.ContextDialer).DialContext(ctx, network, addr)
			},
		}}
		resp, err := httpClient.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}

	if got, err := fetch(); err == nil {
		t.Fatalf("the client got %q with no redirect loaded: the test does not isolate the hop ports", got)
	}

	hop := &service.PortHopService{}
	hop.EnsureHost()
	status := hop.Status()
	wantRule := service.PortHopRule{InboundID: inbound.Id, Remark: "hop-e2e", Ports: hopE2ERange, ToPort: hopE2EPort}
	if !status.Active || status.Error != "" || len(status.Rules) != 1 || status.Rules[0] != wantRule {
		t.Fatalf("status after EnsureHost = %+v", status)
	}
	listing := func() string {
		out, err := hopE2ENft(t, "", "-s", "list", "table", "inet", porthop.TableName)
		if err != nil {
			return ""
		}
		return out
	}
	loaded := listing()
	if want := fmt.Sprintf(`udp dport %s counter name "redirected" redirect to :%d`, hopE2ERange, hopE2EPort); !strings.Contains(loaded, want) {
		t.Fatalf("the loaded table lacks %q:\n%s", want, loaded)
	}

	waitUntil(t, 90*time.Second, "a request through the hopped ports", func() error {
		got, err := fetch()
		if err == nil && got != "hop-ok" {
			err = fmt.Errorf("body %q", got)
		}
		return err
	})
	first := hop.Status().Packets
	if first == 0 {
		t.Fatal("the request succeeded but the redirect counted no flow")
	}
	// Past one hop interval the client is on another port: a new flow, redirected again.
	time.Sleep(12 * time.Second)
	if got, err := fetch(); err != nil || got != "hop-ok" {
		t.Fatalf("after a hop: %q, %v", got, err)
	}
	if after := hop.Status().Packets; after <= first {
		t.Fatalf("redirected flows stayed at %d across a hop interval", after)
	}

	// Applying again changes nothing: once through the service, once as the raw transaction.
	hop.EnsureHost()
	if out, err := hopE2ENft(t, porthop.Ruleset([]porthop.Redirect{{Ranges: []porthop.Range{{From: 21000, To: 21999}}, To: hopE2EPort}}, []uint16{hopE2EPort}), "-f", "-"); err != nil {
		t.Fatalf("reload: %v\n%s", err, out)
	}
	if again := listing(); again != loaded {
		t.Fatalf("a second apply changed the table:\n%s\nwas:\n%s", again, loaded)
	}

	if _, err := inbounds.SetInboundEnable(inbound.Id, false); err != nil {
		t.Fatal(err)
	}
	hop.EnsureHost()
	hop.EnsureHost()
	if left := listing(); left != "" {
		t.Fatalf("the table outlived the disabled inbound:\n%s", left)
	}
	if _, err := inbounds.SetInboundEnable(inbound.Id, true); err != nil {
		t.Fatal(err)
	}
	hop.EnsureHost()
	if listing() != loaded {
		t.Fatal("re-enabling the inbound did not bring the table back")
	}
	if _, err := inbounds.DelInbound(inbound.Id); err != nil {
		t.Fatal(err)
	}
	hop.EnsureHost()
	if left := listing(); left != "" {
		t.Fatalf("the table outlived the deleted inbound:\n%s", left)
	}
}

func waitUntil(t *testing.T, limit time.Duration, what string, try func() error) {
	t.Helper()
	deadline := time.Now().Add(limit)
	var err error
	for time.Now().Before(deadline) {
		if err = try(); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s did not happen within %v: %v", what, limit, err)
}
