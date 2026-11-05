package link

import "testing"

// Probe targets and subscription matching both read the endpoint here: a shape it misses
// leaves that protocol with no probe target and no subscription verdict.
func TestEndpointReadsEveryOutboundShape(t *testing.T) {
	cases := []struct {
		name, link, host string
		port             int
	}{
		{"vless", "vless://11111111-2222-4333-8444-555555555555@vpn.example.com:443?type=tcp&security=none#a", "vpn.example.com", 443},
		{"vmess", "vmess://eyJ2IjoiMiIsInBzIjoiYSIsImFkZCI6InZtLmV4YW1wbGUuY29tIiwicG9ydCI6IjgwODAiLCJpZCI6IjExMTExMTExLTIyMjItNDMzMy04NDQ0LTU1NTU1NTU1NTU1NSIsIm5ldCI6InRjcCJ9", "vm.example.com", 8080},
		{"trojan", "trojan://secret@tr.example.com:8443?security=tls#a", "tr.example.com", 8443},
		{"shadowsocks", "ss://YWVzLTI1Ni1nY206c2VjcmV0@ss.example.com:8388#a", "ss.example.com", 8388},
		{"hysteria", "hysteria2://auth@hy.example.com:4443?sni=hy.example.com#a", "hy.example.com", 4443},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := ParseLink(tc.link)
			if err != nil {
				t.Fatal(err)
			}
			host, port, ok := Endpoint(parsed.Outbound)
			if !ok || host != tc.host || port != tc.port {
				t.Fatalf("Endpoint = %q %d %v, want %q %d", host, port, ok, tc.host, tc.port)
			}
		})
	}
	if _, _, ok := Endpoint(map[string]any{"settings": map[string]any{"servers": []any{}}}); ok {
		t.Fatal("an outbound without a server must have no endpoint")
	}
}
