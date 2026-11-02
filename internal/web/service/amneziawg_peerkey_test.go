package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const awgValidPeerKey = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="

// A client key the device cannot parse is refused when the client is saved,
// instead of failing the whole inbound's device config on every tick.
func TestValidateAmneziaWGClientWriteRejectsMalformedKeys(t *testing.T) {
	inbound := &model.Inbound{
		Protocol: model.AmneziaWG,
		Settings: `{"server":{"privateKey":"x","publicKey":"y","subnetIp":"10.8.1.0","subnetCidr":24},"clients":[]}`,
	}
	cases := map[string]model.Client{
		"public key":    {Email: "a@x", PublicKey: "not-a-key", AllowedIPs: []string{"10.8.1.2/32"}},
		"preshared key": {Email: "a@x", PublicKey: awgValidPeerKey, PreSharedKey: "short", AllowedIPs: []string{"10.8.1.2/32"}},
	}
	for name, c := range cases {
		if err := validateAmneziaWGClientWrite(inbound, []model.Client{c}, ""); err == nil {
			t.Errorf("malformed %s accepted", name)
		}
	}
	good := model.Client{Email: "a@x", PublicKey: awgValidPeerKey, PreSharedKey: awgValidPeerKey, AllowedIPs: []string{"10.8.1.2/32"}}
	if err := validateAmneziaWGClientWrite(inbound, []model.Client{good}, ""); err != nil {
		t.Fatalf("valid keys rejected: %v", err)
	}
}

func TestNormalizeAmneziaWGSettingsRejectsMalformedClientKey(t *testing.T) {
	setupConflictDB(t)
	inbound := &model.Inbound{
		Protocol: model.AmneziaWG,
		Port:     51820,
		Settings: `{"server":{"privateKey":"x","publicKey":"y","subnetIp":"10.8.1.0","subnetCidr":24},` +
			`"clients":[{"email":"a@x","enable":true,"publicKey":"not-a-key","allowedIPs":["10.8.1.2/32"]}]}`,
	}
	err := (&InboundService{}).normalizeAmneziaWGSettings(inbound, nil)
	if err == nil || !strings.Contains(err.Error(), "publicKey") {
		t.Fatalf("malformed client key must be refused at save, got %v", err)
	}
}

// A row stored before the check keeps working with its own key (it can still be
// disabled or edited), but the key cannot be changed to another bad value.
func TestValidateAmneziaWGClientWriteKeepsStoredLegacyKey(t *testing.T) {
	inbound := &model.Inbound{
		Protocol: model.AmneziaWG,
		Settings: `{"server":{"privateKey":"x","publicKey":"y","subnetIp":"10.8.1.0","subnetCidr":24},` +
			`"clients":[{"email":"old@x","enable":true,"publicKey":"legacy-key","allowedIPs":["10.8.1.2/32"]}]}`,
	}
	same := model.Client{Email: "old@x", PublicKey: "legacy-key", AllowedIPs: []string{"10.8.1.2/32"}}
	if err := validateAmneziaWGClientWrite(inbound, []model.Client{same}, "old@x"); err != nil {
		t.Fatalf("an unchanged stored key must not block the edit: %v", err)
	}
	changed := same
	changed.PublicKey = "other-bad-key"
	if err := validateAmneziaWGClientWrite(inbound, []model.Client{changed}, "old@x"); err == nil {
		t.Fatal("a new malformed key was accepted")
	}
}
