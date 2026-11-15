package service

import (
	"encoding/json"
	"testing"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/transport/internet/reality"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// encoding/json, and with it xray-core, matches keys by Unicode simple folding: "ſ" (long s)
// reads as "s". The guard must see such a key as the field the core fills.
func TestPushGuardChecksKeysSpelledWithFoldingRunes(t *testing.T) {
	pushGuardTestService(t)
	stubPushGuardLookup(t, map[string]string{})
	const pk = "aGVsbG9oZWxsb2hlbGxvaGVsbG9oZWxsb2hlbGxvMTI"
	realityStream := func(key, extra string) string {
		return `{"network":"tcp","security":"reality","` + key + `":{"target":"127.0.0.1:62789"` + extra + `,"serverNames":["a.com"],"privateKey":"` + pk + `","shortIds":[""]}}`
	}
	cases := map[string]*model.Inbound{
		"realitySettingſ": {Port: 44902, Protocol: model.VLESS, Settings: vlessSettings, StreamSettings: realityStream("realitySettingſ", "")},
		"fallbackſ": {Port: 44905, Protocol: model.VLESS, StreamSettings: `{"network":"tcp"}`,
			Settings: `{"clients":[],"decryption":"none","fallbackſ":[{"dest":"127.0.0.1:62789"}]}`},
		"maſterKeyLog": {Port: 44904, Protocol: model.VLESS, Settings: vlessSettings,
			StreamSettings: `{"network":"tcp","security":"reality","realitySettings":{"target":"1.1.1.1:443","maſterKeyLog":"/etc/cron.d/zz","serverNames":["a.com"],"privateKey":"` + pk + `","shortIds":[""]}}`},
	}
	for name, ib := range cases {
		if err := validatePushedInbound(ib, nil); err == nil {
			t.Errorf("%s: pushed inbound accepted, want refused", name)
		}
	}

	// The spelling is live, not ignored: the core builds REALITY towards the loopback target.
	raw, err := json.Marshal(cases["realitySettingſ"].GenXrayInboundConfig())
	if err != nil {
		t.Fatal(err)
	}
	built, err := xray.BuildInbound(raw, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	rc, err := built.ReceiverSettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := rc.(*proxyman.ReceiverConfig).StreamSettings.GetEffectiveSecuritySettings()
	if r, ok := sec.(*reality.Config); !ok || r.Dest != "127.0.0.1:62789" {
		t.Fatalf("core security %T %+v, want REALITY to 127.0.0.1:62789 (the folded key must be the one the core reads)", sec, sec)
	}
}

func TestFoldJSONKey(t *testing.T) {
	for in, want := range map[string]string{"realitySettingſ": "realitysettings", "REALITYSETTINGS": "realitysettings", "Key": "key", "realıty": "realıty"} {
		if got := foldJSONKey(in); got != want {
			t.Errorf("foldJSONKey(%q) = %q, want %q", in, got, want)
		}
	}
}
