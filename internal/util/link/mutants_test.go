package link

import (
	"encoding/base64"
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
