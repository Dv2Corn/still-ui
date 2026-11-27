package link

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Boundary tests written against surviving mutants reported by gremlins (nightly mutation run).

func TestKcpParamInRangeIncludesBothBounds(t *testing.T) {
	cases := []struct {
		in       string
		min, max int
		ok       bool
	}{
		{"21", kcpMinMTU, kcpMaxMTU, true},
		{"20", kcpMinMTU, kcpMaxMTU, false},
		{"10", kcpMinTTI, kcpMaxTTI, true},
		{"1000", kcpMinTTI, kcpMaxTTI, true},
		{"1001", kcpMinTTI, kcpMaxTTI, false},
	}
	for _, c := range cases {
		if _, ok := kcpParamInRange(c.in, c.min, c.max); ok != c.ok {
			t.Errorf("kcpParamInRange(%s, %d, %d) ok = %v, want %v", c.in, c.min, c.max, ok, c.ok)
		}
	}
}

func TestClampQuicNumericAcceptsTheCeilingItself(t *testing.T) {
	if v, ok := clampQuicNumeric("maxIncomingStreams", quicNumericMax); !ok || v != quicNumericMax {
		t.Fatalf("at the ceiling: got (%v, %v), want (%v, true)", v, ok, quicNumericMax)
	}
	if _, ok := clampQuicNumeric("maxIncomingStreams", quicNumericMax+1); ok {
		t.Fatal("above the ceiling must be rejected")
	}
}

func vlessLines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "vless://uuid@1.2.3.4:%d?type=tcp&security=none#n%d\n", 1000+i, i)
	}
	return b.String()
}

func TestParseSubscriptionBodyStopsExactlyAtTheCap(t *testing.T) {
	for _, n := range []int{MaxSubscriptionOutbounds, MaxSubscriptionOutbounds + 1} {
		outs, ids, err := ParseSubscriptionBody([]byte(vlessLines(n)))
		if err != nil {
			t.Fatal(err)
		}
		if len(outs) != MaxSubscriptionOutbounds || len(ids) != MaxSubscriptionOutbounds {
			t.Fatalf("%d links: got %d outbounds, want %d", n, len(outs), MaxSubscriptionOutbounds)
		}
	}
}

func TestParseSubscriptionBodyNumbersRepeatedIdentities(t *testing.T) {
	link := "vless://uuid@1.2.3.4:443?type=tcp&security=none#same\n"
	_, ids, err := ParseSubscriptionBody([]byte(strings.Repeat(link, 3)))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("got %d identities, want 3", len(ids))
	}
	want := []string{ids[0], ids[0] + "#1", ids[0] + "#2"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("identities = %q, want %q", ids, want)
		}
	}
	if strings.Contains(ids[0], "#") {
		t.Fatalf("the first copy keeps the plain identity, got %q", ids[0])
	}
}

func TestParseSubscriptionBodyBase64WithInsertedWhitespace(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString([]byte(vlessLines(2)))
	var b strings.Builder
	for i, r := range enc {
		if i > 0 && i%10 == 0 {
			b.WriteString([]string{"\n", " ", "\t", "\r\n"}[i/10%4])
		}
		b.WriteRune(r)
	}
	outs, _, err := ParseSubscriptionBody([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 2 {
		t.Fatalf("got %d outbounds from base64 with inserted whitespace, want 2", len(outs))
	}
}

func TestNestsWithinRejectsOneLevelPastTheLimit(t *testing.T) {
	if !nestsWithin(map[string]any{"leaf": nil}, 1) {
		t.Fatal("a value exactly at the remaining depth must be accepted")
	}
	if nestsWithin(map[string]any{"child": map[string]any{"leaf": nil}}, 1) {
		t.Fatal("a child below the remaining depth must be rejected")
	}
	if nestsWithin([]any{[]any{nil}}, 1) {
		t.Fatal("nested arrays must obey the same depth limit")
	}
}

func TestSlugRemarkTruncatesAtRuneBoundary(t *testing.T) {
	got := SlugRemark(strings.Repeat("я", maxSlugRunes+1))
	if got != strings.Repeat("я", maxSlugRunes) {
		t.Fatalf("SlugRemark length/value = %d/%q, want %d Cyrillic runes", len([]rune(got)), got, maxSlugRunes)
	}
}

func TestEndpointPortBounds(t *testing.T) {
	for _, c := range []struct {
		port float64
		ok   bool
	}{{0, false}, {1, true}, {65535, true}, {65536, false}} {
		ob := map[string]any{"settings": map[string]any{"address": "1.2.3.4", "port": c.port}}
		if _, _, ok := Endpoint(ob); ok != c.ok {
			t.Errorf("Endpoint(port %v) ok = %v, want %v", c.port, ok, c.ok)
		}
	}
}

func vmessLink(t *testing.T, j map[string]any) *ParseResult {
	t.Helper()
	b, _ := json.Marshal(j)
	res, err := ParseLink("vmess://" + base64.StdEncoding.EncodeToString(b))
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	return res
}

func vmessUser(res *ParseResult) map[string]any {
	vnext := res.Outbound["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)
	return vnext["users"].([]any)[0].(map[string]any)
}

func TestParseVmessFields(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"v": "2", "ps": "a", "add": "1.2.3.4", "port": "443", "id": "uuid", "net": "tcp"}
	}
	for scy, want := range map[string]string{"none": "auto", "zero": "auto", "aes-128-gcm": "aes-128-gcm"} {
		j := base()
		j["scy"] = scy
		if got := vmessUser(vmessLink(t, j))["security"]; got != want {
			t.Errorf("scy %q -> %v, want %q", scy, got, want)
		}
	}

	j := base()
	j["type"] = "http"
	j["path"] = "/p"
	stream := vmessLink(t, j).Outbound["streamSettings"].(map[string]any)
	if tcp, _ := stream["tcpSettings"].(map[string]any); tcp == nil || tcp["header"].(map[string]any)["type"] != "http" {
		t.Errorf("tcp type http: tcpSettings = %v", stream["tcpSettings"])
	}

	j = base()
	j["net"], j["mode"] = "xhttp", "packet-up"
	stream = vmessLink(t, j).Outbound["streamSettings"].(map[string]any)
	if got := stream["xhttpSettings"].(map[string]any)["mode"]; got != "packet-up" {
		t.Errorf("xhttp mode = %v, want packet-up", got)
	}

	j = base()
	j["tls"], j["alpn"] = "tls", "h2,http/1.1"
	stream = vmessLink(t, j).Outbound["streamSettings"].(map[string]any)
	if got := fmt.Sprint(stream["tlsSettings"].(map[string]any)["alpn"]); got != "[h2 http/1.1]" {
		t.Errorf("alpn = %s, want [h2 http/1.1]", got)
	}
}

func TestVmessIdentityIgnoresOnlyTheRemark(t *testing.T) {
	a := map[string]any{"ps": "one", "add": "1.2.3.4", "port": "443", "id": "uuid"}
	b := map[string]any{"ps": "two", "add": "1.2.3.4", "port": "443", "id": "uuid"}
	c := map[string]any{"ps": "one", "add": "1.2.3.5", "port": "443", "id": "uuid"}
	if vmessIdentity(a) != vmessIdentity(b) {
		t.Error("links that differ only in ps must share an identity")
	}
	if vmessIdentity(a) == vmessIdentity(c) {
		t.Error("links with different servers must not share an identity")
	}
}

func TestParseWireguardFields(t *testing.T) {
	res, err := ParseLink("wg://secret@1.2.3.4:51820?publickey=pub&address=10.0.0.2/32&allowedips=10.0.0.0/8&psk=shared&keepalive=25&mtu=1280&reserved=1,2,3#w")
	if err != nil {
		t.Fatalf("ParseLink(wg://): %v", err)
	}
	s := res.Outbound["settings"].(map[string]any)
	peer := s["peers"].([]any)[0].(map[string]any)
	checks := map[string][2]any{
		"address":      {fmt.Sprint(s["address"]), "[10.0.0.2/32]"},
		"allowedIPs":   {fmt.Sprint(peer["allowedIPs"]), "[10.0.0.0/8]"},
		"preSharedKey": {peer["preSharedKey"], "shared"},
		"keepAlive":    {peer["keepAlive"], 25},
		"mtu":          {s["mtu"], 1280},
		"reserved":     {fmt.Sprint(s["reserved"]), "[1 2 3]"},
	}
	for k, v := range checks {
		if v[0] != v[1] {
			t.Errorf("%s = %v, want %v", k, v[0], v[1])
		}
	}

	res, err = ParseLink("wireguard://secret@1.2.3.4:51820?publickey=pub#w")
	if err != nil {
		t.Fatalf("ParseLink(wireguard://): %v", err)
	}
	s = res.Outbound["settings"].(map[string]any)
	peer = s["peers"].([]any)[0].(map[string]any)
	if fmt.Sprint(s["address"]) != "[0.0.0.0/0 ::/0]" || fmt.Sprint(peer["allowedIPs"]) != "[0.0.0.0/0 ::/0]" {
		t.Errorf("defaults: address=%v allowedIPs=%v", s["address"], peer["allowedIPs"])
	}
	for _, k := range []string{"preSharedKey", "keepAlive"} {
		if _, ok := peer[k]; ok {
			t.Errorf("peer has %q without the parameter", k)
		}
	}
	for _, k := range []string{"mtu", "reserved"} {
		if _, ok := s[k]; ok {
			t.Errorf("settings have %q without the parameter", k)
		}
	}
}

func TestParseShadowsocksEdgeForms(t *testing.T) {
	user := base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:pass"))
	_, err := ParseLink("ss://" + user + "@1.2.3.4:abc#x")
	if err == nil || !strings.HasPrefix(err.Error(), `bad ss port "abc":`) {
		t.Errorf("bad port error = %v, want it to start with bad ss port \"abc\"", err)
	}
	legacy := base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:pass@1.2.3.4:abc"))
	_, err = ParseLink("ss://" + legacy + "#x")
	if err == nil || !strings.HasPrefix(err.Error(), `bad legacy ss port "abc":`) {
		t.Errorf("bad legacy port error = %v, want it to start with bad legacy ss port \"abc\"", err)
	}
	res, err := ParseLink("ss://" + user + "@1.2.3.4:443#x")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Outbound["streamSettings"].(map[string]any)["network"]; got != "tcp" {
		t.Errorf("default network = %v, want tcp", got)
	}
}
