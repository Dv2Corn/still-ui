package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func realityFingerprintOf(t *testing.T, streamSettings string) (string, bool) {
	t.Helper()
	var stream struct {
		RealitySettings struct {
			Settings map[string]any `json:"settings"`
		} `json:"realitySettings"`
	}
	if err := json.Unmarshal([]byte(streamSettings), &stream); err != nil {
		t.Fatalf("stream settings are not JSON: %v", err)
	}
	fp, ok := stream.RealitySettings.Settings["fingerprint"].(string)
	return fp, ok
}

func TestStillNewRealityFingerprintDefault(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"missing fingerprint", `{"security":"reality","realitySettings":{"settings":{"publicKey":"k"}}}`, "ios"},
		{"empty fingerprint", `{"security":"reality","realitySettings":{"settings":{"publicKey":"k","fingerprint":""}}}`, "ios"},
		{"missing client settings", `{"security":"reality","realitySettings":{"target":"example.com:443"}}`, "ios"},
		{"explicit chrome kept", `{"security":"reality","realitySettings":{"settings":{"fingerprint":"chrome"}}}`, "chrome"},
		{"explicit firefox kept", `{"security":"reality","realitySettings":{"settings":{"fingerprint":"firefox"}}}`, "firefox"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := realityFingerprintOf(t, applyNewRealityFingerprintDefault(tc.in))
			if got != tc.want {
				t.Fatalf("fingerprint = %q, want %q", got, tc.want)
			}
		})
	}
	for _, untouched := range []string{
		"",
		`not json`,
		`{"security":"tls","tlsSettings":{"settings":{"fingerprint":""}}}`,
		`{"security":"none"}`,
	} {
		if out := applyNewRealityFingerprintDefault(untouched); out != untouched {
			t.Fatalf("non-REALITY stream settings changed: %q -> %q", untouched, out)
		}
	}
}

// Through AddInbound: a REALITY inbound posted without a fingerprint is stored
// with ios; an inbound that names one keeps it.
func TestStillAddInboundRealityGetsIOSFingerprint(t *testing.T) {
	setupBulkDB(t)
	inboundSvc := &InboundService{}
	stream := func(fp string) string {
		settings := `{"publicKey":"E59WjnvZcQMu7tR7_BgyhycuEdBS-CtKxfImRCdAvFM","serverName":"","spiderX":"/"` + fp + `}`
		return `{"network":"tcp","security":"reality","realitySettings":{"show":false,"xver":0,"target":"www.example.com:443","serverNames":["www.example.com"],"privateKey":"aGLs0bLZgIIZvP0ZW5bZkTq7yH7O6WkVRXnW7f7a1kE","shortIds":["6ba85179e30d4fc2"],"settings":` + settings + `},"tcpSettings":{"header":{"type":"none"}}}`
	}
	add := func(port int, fp string) string {
		ib, _, err := inboundSvc.AddInbound(&model.Inbound{
			UserId: 1, Enable: true, Port: port, Protocol: model.VLESS,
			Settings:       `{"clients":[{"id":"0b2a8f3e-6a0e-4c1e-9b7a-2f6f6f0` + fmt.Sprint(port) + `","email":"fp-` + fmt.Sprint(port) + `@reality","enable":true,"flow":"xtls-rprx-vision"}],"decryption":"none"}`,
			StreamSettings: stream(fp),
		})
		if err != nil {
			t.Fatalf("AddInbound: %v", err)
		}
		stored, err := inboundSvc.GetInbound(ib.Id)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := realityFingerprintOf(t, stored.StreamSettings)
		return got
	}
	if got := add(44301, ""); got != "ios" {
		t.Fatalf("new REALITY inbound fingerprint = %q, want ios", got)
	}
	if got := add(44302, `,"fingerprint":"chrome"`); got != "chrome" {
		t.Fatalf("explicit fingerprint replaced: %q", got)
	}
}
