package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func kernelAWGSubnetSettings(subnet string, kernel bool) string {
	s := `{"server":{"privateKey":"x","publicKey":"y","subnetIp":"` + subnet + `","subnetCidr":24`
	if kernel {
		s += `,"kernelMode":true`
	}
	return s + `},"clients":[]}`
}

// Two kernel-mode inbounds on one subnet each get an interface address in it;
// the host routes the subnet through the first, so the second's return traffic
// is lost. Such a save is refused; embedded inbounds have their own stacks.
func TestNormalizeAmneziaWGSettingsRefusesOverlappingKernelSubnets(t *testing.T) {
	setupConflictDB(t)
	existing := &model.Inbound{Tag: "awg-k1", Enable: true, Port: 51820, Protocol: model.AmneziaWG,
		Settings: kernelAWGSubnetSettings("10.8.1.0", true)}
	if err := database.GetDB().Create(existing).Error; err != nil {
		t.Fatal(err)
	}
	svc := &InboundService{}
	try := func(settings string, enable bool) error {
		ib := &model.Inbound{Protocol: model.AmneziaWG, Port: 51821, Enable: enable, Settings: settings}
		return svc.normalizeAmneziaWGSettings(ib, nil)
	}
	for name, settings := range map[string]string{
		"same subnet":  kernelAWGSubnetSettings("10.8.1.0", true),
		"wider subnet": strings.Replace(kernelAWGSubnetSettings("10.8.0.0", true), `"subnetCidr":24`, `"subnetCidr":16`, 1),
	} {
		if err := try(settings, true); err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Errorf("%s: want an overlap refusal, got %v", name, err)
		}
	}
	for name, c := range map[string]struct {
		settings string
		enable   bool
	}{
		"other subnet":         {kernelAWGSubnetSettings("10.8.2.0", true), true},
		"embedded same subnet": {kernelAWGSubnetSettings("10.8.1.0", false), true},
		"disabled same subnet": {kernelAWGSubnetSettings("10.8.1.0", true), false},
	} {
		if err := try(c.settings, c.enable); err != nil && strings.Contains(err.Error(), "overlaps") {
			t.Errorf("%s: refused as overlapping: %v", name, err)
		}
	}
	// Re-saving the existing row itself is not a conflict with itself.
	self := *existing
	if err := svc.normalizeAmneziaWGSettings(&self, existing); err != nil && strings.Contains(err.Error(), "overlaps") {
		t.Errorf("re-saving the same inbound refused: %v", err)
	}
}
