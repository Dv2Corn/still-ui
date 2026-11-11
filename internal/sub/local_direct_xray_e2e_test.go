package sub

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	xraygeodata "github.com/xtls/xray-core/common/geodata"

	"github.com/mhsanaei/3x-ui/v3/internal/sub/localdirect"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

var xrayAccessRoute = regexp.MustCompile(`accepted \w+:(\S+):\d+ \[\S+ (?:->|>>) (\S+)\]`)

func socksRequest(t *testing.T, proxy, host string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxy, 2*time.Second)
	if err != nil {
		t.Fatalf("dial xray socks inbound: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	greeting := make([]byte, 2)
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("socks greeting: %v", err)
	}
	if _, err := io.ReadFull(conn, greeting); err != nil {
		t.Fatalf("socks greeting reply: %v", err)
	}
	request, payload := []byte{5, 1, 0}, "ping\n"
	if ip := net.ParseIP(host).To4(); ip != nil {
		request = append(append(request, 1), ip...)
	} else {
		request = append(append(request, 3, byte(len(host))), host...)
		payload = "GET / HTTP/1.1\r\nHost: " + host + "\r\n\r\n"
	}
	if _, err := conn.Write(append(request, 0, 80)); err != nil {
		t.Fatalf("socks connect %s: %v", host, err)
	}
	_, _ = io.ReadFull(conn, make([]byte, 10))
	_, _ = conn.Write([]byte(payload))
	_, _ = io.Copy(io.Discard, conn)
}

// TestLocalDirectRoutesInRealXray feeds the JSON subscription to a real core and reads its routing
// decisions from the access log. Needs XRAY_E2E_BINARY: an xray built from go.mod's xray-core.
func TestLocalDirectRoutesInRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_E2E_BINARY")
	if bin == "" {
		t.Skip("set XRAY_E2E_BINARY to an xray built from go.mod's xray-core (go build github.com/xtls/xray-core/main)")
	}

	seedLocalDirectSub(t)
	assets := t.TempDir()
	t.Setenv("XRAY_LOCATION_ASSET", assets)
	writeGeoFile(t, assets, localdirect.GeositeFile, &xraygeodata.GeoSiteList{Entry: []*xraygeodata.GeoSite{
		{Code: "CATEGORY-RU", Domain: []*xraygeodata.Domain{{Type: xraygeodata.Domain_Domain, Value: "yandex.net"}}},
	}})
	// Telegram's range is listed as Russian here, as it is in the real databases.
	writeGeoFile(t, assets, localdirect.GeoIPFile, &xraygeodata.GeoIPList{Entry: []*xraygeodata.GeoIP{
		{Code: "RU", Cidr: []*xraygeodata.CIDR{{Ip: []byte{203, 0, 113, 0}, Prefix: 24}, {Ip: []byte{91, 108, 0, 0}, Prefix: 16}}},
	}})
	saveLocalDirectCountry(t, "ru")
	router := newLocalDirectRouter(WithSUBLocalDirect(loadLocalDirect(&service.SettingService{})))

	var configs []map[string]any
	if err := json.Unmarshal(localDirectGet(router, "/json/s1", "").Body.Bytes(), &configs); err != nil || len(configs) == 0 {
		t.Fatalf("JSON subscription: %d configs, %v", len(configs), err)
	}
	config := configs[0]
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := listener.Addr().String()
	_ = listener.Close()
	inbound := config["inbounds"].([]any)[0].(map[string]any)
	inbound["port"] = listener.Addr().(*net.TCPAddr).Port
	config["inbounds"] = []any{inbound}
	// Same tags, no network: the test reads where each connection was sent, it does not send it.
	config["outbounds"] = []any{
		map[string]any{"tag": "proxy", "protocol": "blackhole"},
		map[string]any{"tag": "direct", "protocol": "blackhole"},
		map[string]any{"tag": "block", "protocol": "blackhole"},
	}
	config["dns"].(map[string]any)["hosts"] = map[string]any{
		"shop.example.com":   "203.0.113.7",
		"tg.example.org":     "91.108.4.4",
		"lan.example.com":    "192.168.1.5",
		"abroad.example.com": "198.51.100.9",
	}
	dir := t.TempDir()
	accessLog := filepath.Join(dir, "access.log")
	config["log"] = map[string]any{"access": accessLog, "loglevel": "warning"}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "run", "-c", configPath)
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+assets)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start xray: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if conn, err := net.DialTimeout("tcp", proxy, time.Second); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("xray did not open its socks inbound: the generated config was refused or the core crashed")
		}
	}

	want := []struct{ host, outbound, why string }{
		{"foo.ru", "direct", "country zone"},
		{"yandex.net", "direct", "country geosite category"},
		{"telegram.org", "proxy", "always-VPN domain"},
		{"shop.example.com", "direct", "foreign zone, resolves into the country"},
		{"tg.example.org", "proxy", "resolves into an always-VPN range that geoip calls Russian"},
		{"lan.example.com", "direct", "resolves into a private network"},
		{"abroad.example.com", "proxy", "resolves abroad"},
		{"192.168.1.1", "direct", "private address"},
		{"203.0.113.9", "direct", "address in the country"},
		{"91.108.5.5", "proxy", "always-VPN range that geoip calls Russian"},
		{"198.51.100.7", "proxy", "address abroad"},
	}
	for _, tt := range want {
		socksRequest(t, proxy, tt.host)
	}

	routed := map[string]string{}
	for deadline := time.Now().Add(5 * time.Second); len(routed) < len(want) && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		logged, _ := os.ReadFile(accessLog)
		for _, match := range xrayAccessRoute.FindAllStringSubmatch(string(logged), -1) {
			routed[match[1]] = strings.TrimSuffix(match[2], "]")
		}
	}
	for _, tt := range want {
		if routed[tt.host] != tt.outbound {
			t.Errorf("%s (%s): xray sent it to %q, want %q", tt.host, tt.why, routed[tt.host], tt.outbound)
		}
	}
}
