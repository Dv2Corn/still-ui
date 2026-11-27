package model

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Boundary tests written against surviving mutants reported by gremlins (nightly mutation run).

func TestKeepAliveValueValidateRanges(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
	}{
		{"1-5", true},
		{"0-0", true},
		{"0-65535", true},
		{"0-65536", false},
		{"5-1", false},
		{"01-5", false},
		{"1-05", false},
		{"a-5", false},
		{"1-b", false},
	} {
		if err := KeepAliveValue(c.in).Validate(true); (err == nil) != c.ok {
			t.Errorf("Validate(%q) error = %v, want ok=%v", c.in, err, c.ok)
		}
	}
}

func TestKeepAliveValueUnmarshalEmptyInputIsAnError(t *testing.T) {
	var v KeepAliveValue
	if err := v.UnmarshalJSON([]byte{}); err == nil {
		t.Fatal("empty input must be rejected")
	}
}

func TestWireguardPeerFromClientOmitsEmptyValues(t *testing.T) {
	peer := WireguardPeerFromClient(Client{Email: "a"})
	for _, k := range []string{"allowedIPs", "keepAlive", "publicKey", "preSharedKey"} {
		if _, ok := peer[k]; ok {
			t.Errorf("peer has %q for an empty value: %v", k, peer)
		}
	}
	ka := KeepAliveValue("25")
	peer = WireguardPeerFromClient(Client{Email: "a", AllowedIPs: []string{"10.0.0.2/32"}, KeepAlive: &ka})
	if _, ok := peer["allowedIPs"]; !ok {
		t.Errorf("peer lost allowedIPs: %v", peer)
	}
	if peer["keepAlive"] != 25 {
		t.Errorf("peer keepAlive = %v, want 25", peer["keepAlive"])
	}
}

func TestStripVlessInboundEncryption(t *testing.T) {
	out, changed := StripVlessInboundEncryption(`{"clients":[],"encryption":"none"}`)
	if !changed || strings.Contains(out, "encryption") || !strings.Contains(out, "clients") {
		t.Fatalf("strip = (%q, %v)", out, changed)
	}
	for _, in := range []string{"", "{", `{"clients":[]}`} {
		if out, changed := StripVlessInboundEncryption(in); changed || out != in {
			t.Errorf("StripVlessInboundEncryption(%q) = (%q, %v), want unchanged", in, out, changed)
		}
	}
}

func TestMtprotoSecretMiddleAcceptsExactly32HexChars(t *testing.T) {
	mid := strings.Repeat("ab", 16)
	if got := mtprotoSecretMiddle("ee" + mid); got != mid {
		t.Fatalf("mtprotoSecretMiddle = %q, want %q", got, mid)
	}
}

func TestInboundEmptyListenBindsAllAddresses(t *testing.T) {
	cfg := (&Inbound{Protocol: "vless", Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}).GenXrayInboundConfig()
	if string(cfg.Listen) != `"0.0.0.0"` {
		t.Fatalf("listen = %s, want \"0.0.0.0\"", cfg.Listen)
	}
}

func TestClientReverseRoundTrip(t *testing.T) {
	if rec := (&Client{Email: "a"}).ToRecord(); rec.Reverse != "" {
		t.Fatalf("client without reverse stored %q", rec.Reverse)
	}
	c := (&ClientRecord{Email: "a", Reverse: `{"tag":"r"}`}).ToClient()
	if c.Reverse == nil || c.Reverse.Tag != "r" {
		t.Fatalf("reverse = %+v, want tag r", c.Reverse)
	}
}

func TestMtprotoSecretDomain(t *testing.T) {
	mid := strings.Repeat("ab", 16)
	dom := hex.EncodeToString([]byte("example.com"))
	for secret, want := range map[string]string{
		"ee" + mid + dom:  "example.com",
		"ee" + mid:        "",
		"ee" + mid[:30]:   "",
		"ee" + mid + "zz": "",
	} {
		if got := mtprotoSecretDomain(secret); got != want {
			t.Errorf("mtprotoSecretDomain(%q) = %q, want %q", secret, got, want)
		}
	}
}
