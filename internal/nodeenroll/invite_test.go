package nodeenroll

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "abcdefghijABCDEFGHIJ0123456789abcdefghijABCDEFGH"

func TestInviteRoundTrip(t *testing.T) {
	in := Invite{Token: testToken, KeyPin: bytes.Repeat([]byte{0xfe}, 32), MasterURL: "https://[2001:db8::1]:2053/a-b_c/"}
	s := in.String()
	if !strings.HasPrefix(s, Prefix+"."+testToken+".") || strings.ContainsAny(s, " +/=") {
		t.Fatalf("invitation %q is not one URL-safe line", s)
	}
	out, err := Parse("  " + s + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if out.Token != in.Token || !bytes.Equal(out.KeyPin, in.KeyPin) || out.MasterURL != in.MasterURL {
		t.Fatalf("round trip %+v", out)
	}
}

func TestParseRefusesWhatIsNotAnInvitation(t *testing.T) {
	enc := base64.RawURLEncoding
	pin := enc.EncodeToString(bytes.Repeat([]byte{1}, 32))
	url := enc.EncodeToString([]byte("https://m.example.com:2053/p/"))
	cases := map[string]string{
		"wrong prefix":    "still-node2." + testToken + "." + pin + "." + url,
		"short token":     Prefix + ".abc." + pin + "." + url,
		"short pin":       Prefix + "." + testToken + "." + enc.EncodeToString([]byte{1, 2}) + "." + url,
		"plain http":      Prefix + "." + testToken + "." + pin + "." + enc.EncodeToString([]byte("http://m.example.com/")),
		"no trailing /":   Prefix + "." + testToken + "." + pin + "." + enc.EncodeToString([]byte("https://m.example.com/p")),
		"shell metachars": Prefix + "." + testToken + "." + pin + "." + enc.EncodeToString([]byte("https://m.example.com/$(id)/")),
		"extra field":     Prefix + "." + testToken + "." + pin + "." + url + ".x",
		"empty":           "",
	}
	for name, s := range cases {
		if _, err := Parse(s); !errors.Is(err, ErrInvalidInvite) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNormalizeMasterURL(t *testing.T) {
	ok := map[string]string{
		"https://m.example.com":             "https://m.example.com/",
		"https://m.example.com:2053/path":   "https://m.example.com:2053/path/",
		"https://1.2.3.4:2053/p/?x=1#frag":  "https://1.2.3.4:2053/p/",
		" https://m.example.com:443/a/b/  ": "https://m.example.com:443/a/b/",
	}
	for in, want := range ok {
		if got, err := NormalizeMasterURL(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	refused := map[string]string{
		"http://m.example.com/":          "must be https://",
		"https://user:pw@m.example.com/": "must be https://",
		"https://m.example.com:99999/":   "invalid port",
		"ftp://x/":                       "must be https://",
		"https:///p":                     "must be https://",
	}
	for in, want := range refused {
		if _, err := NormalizeMasterURL(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want the error %q", in, err, want)
		}
	}
}

func newMaster(t *testing.T, h http.HandlerFunc) (*httptest.Server, Invite) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv, Invite{Token: testToken, KeyPin: KeyPin(srv.Certificate()), MasterURL: srv.URL + "/base/"}
}

func TestClientPinsTheMasterKey(t *testing.T) {
	var got *http.Request
	_, inv := newMaster(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"n1","scope":"node-sync","expiresAt":42}`))
	})
	check, err := NewClient(inv).Check(context.Background())
	if err != nil {
		t.Fatalf("self-signed master with the right pin: %v", err)
	}
	if check.Scope != "node-sync" || got.URL.Path != "/base/"+AgentPath || got.Header.Get("Authorization") != "Bearer "+testToken {
		t.Fatalf("check %+v, request %s %v", check, got.URL.Path, got.Header)
	}

	got = nil
	wrong := inv
	wrong.KeyPin = bytes.Repeat([]byte{9}, 32)
	if _, err := NewClient(wrong).Check(context.Background()); !errors.Is(err, ErrKeyPinMismatch) {
		t.Fatalf("wrong pin: %v", err)
	}
	if got != nil {
		t.Fatal("the token reached a server whose key does not match")
	}
}

func TestClientDoesNotFollowRedirectsAndReportsRefusals(t *testing.T) {
	_, inv := newMaster(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Redirect(w, r, "https://elsewhere.example.com/steal", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"the master could not reach the node panel"}`))
	})
	c := NewClient(inv)
	var se *StatusError
	if _, err := c.Check(context.Background()); !errors.As(err, &se) || se.Status != http.StatusFound {
		t.Fatalf("redirect: %v", err)
	}
	_, err := c.Register(context.Background(), RegisterRequest{Port: 1})
	if !errors.As(err, &se) || se.Status != http.StatusBadGateway || !strings.Contains(se.Message, "could not reach") {
		t.Fatalf("refusal: %v", err)
	}
}
