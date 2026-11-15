package service

import (
	"net/http"
	"slices"
	"testing"
)

func TestSubLeakIPs(t *testing.T) {
	cases := []struct {
		name, remote, realIP, xff string
		want                      []string
	}{
		{"direct client ignores headers", "198.51.100.7:4000", "1.1.1.1", "8.8.8.8", []string{"198.51.100.7"}},
		{"proxy sets only X-Forwarded-For", "127.0.0.1:4000", "", "203.0.113.5", []string{"203.0.113.5"}},
		{"proxy sets only X-Real-IP", "127.0.0.1:4000", "203.0.113.5", "", []string{"203.0.113.5"}},
		{"headers agree", "127.0.0.1:4000", "203.0.113.5", "203.0.113.5", []string{"203.0.113.5"}},
		// nginx appends the peer to X-Forwarded-For and passes the client's own X-Real-IP through:
		// RemoteIP falls back to the proxy, but the leak count must still see the real network.
		{"client X-Real-IP beside the proxy's X-Forwarded-For", "127.0.0.1:4000", "1.1.1.1", "203.0.113.5", []string{"1.1.1.1", "203.0.113.5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodGet, "/sub/x", nil)
			r.RemoteAddr = tc.remote
			if tc.realIP != "" {
				r.Header.Set("X-Real-IP", tc.realIP)
			}
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := SubLeakIPs(r); !slices.Equal(got, tc.want) {
				t.Fatalf("SubLeakIPs = %v, want %v (RemoteIP %q)", got, tc.want, RemoteIP(r))
			}
		})
	}
}
