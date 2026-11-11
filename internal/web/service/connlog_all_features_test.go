package service

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/connlog"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func generatedConfigBytes(t *testing.T) []byte {
	t.Helper()
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// With the release's other features in use (hop redirect, device limit, local sites direct, old
// links), the connection log turned on and off again leaves the generated Xray config unchanged.
func TestConnLogOffKeepsTheGeneratedConfigWithTheOtherFeaturesOn(t *testing.T) {
	connLogPanel(t)
	useFakePortHopHost(t)
	inbounds := &InboundService{}
	if _, _, err := inbounds.AddInbound(hopInbound(8443, "20000-20100", true)); err != nil {
		t.Fatalf("add the hop inbound: %v", err)
	}
	vless := &model.Inbound{
		Enable: true, Port: 4443, Protocol: model.VLESS,
		StreamSettings: `{"network":"tcp","security":"none"}`,
		Settings:       `{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"limited@example.com","subId":"sub-limited","limitHwid":2,"enable":true}],"decryption":"none"}`,
	}
	if _, _, err := inbounds.AddInbound(vless); err != nil {
		t.Fatalf("add the VLESS inbound: %v", err)
	}
	if err := database.GetDB().Create(&model.LegacySubUser{Username: "old-user", SubID: "sub-limited"}).Error; err != nil {
		t.Fatal(err)
	}
	settings := &SettingService{}
	all, err := settings.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	all.SubLocalDirect, all.SubHwidDefaultLimit, all.SubHwidExpireDays = "ru", 3, 30
	if err := settings.UpdateAllSetting(all, SecretClears{}); err != nil {
		t.Fatalf("save the subscription settings: %v", err)
	}

	before := generatedConfigBytes(t)
	for _, want := range []string{`"udpHop"`, `"redirect":true`, "limited@example.com"} {
		if !bytes.Contains(before, []byte(want)) {
			t.Fatalf("the generated config lacks %s, so the test would compare nothing: %s", want, before)
		}
	}

	setConnLog(t, true, connlog.DestinationFull, connlog.SourceFull)
	on := generatedConfigBytes(t)
	if bytes.Equal(on, before) || !bytes.Contains(on, []byte(connLogAccessLogName)) {
		t.Fatalf("the connection log on did not turn the access log on: %s", on)
	}

	setConnLog(t, false, connlog.DestinationFull, connlog.SourceFull)
	if off := generatedConfigBytes(t); !bytes.Equal(off, before) {
		t.Fatalf("the connection log off changed the generated config:\nbefore: %s\nafter:  %s", before, off)
	}
}
