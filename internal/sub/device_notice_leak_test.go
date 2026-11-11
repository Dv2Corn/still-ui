package sub

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const (
	refusedNotice     = "Device limit 1 reached."
	refusedRealityPBK = "PBKrefusedDeviceMustNotSeeThisKey"
	refusedShortID    = "5eaf00d1"
	refusedHostAddr   = "front.hidden.example.com"
	refusedExtServer  = "external.hidden.example.com"
	refusedExtSecret  = "external-trojan-secret"
	// A template naming the client's proxies is what once put a hidden one back.
	refusedClashRules = "proxy-groups:\n  - name: PROXY\n    type: select\n    proxies: [a-a@e, b-b@e]\n"
)

// refusedDeviceSecrets is everything of the subscription a refused device must not learn.
var refusedDeviceSecrets = []string{
	"203.0.113.5",
	"11111111-2222-4333-8444-",
	refusedRealityPBK,
	refusedShortID,
	"reality.hidden.example.com",
	refusedHostAddr,
	refusedExtServer,
	refusedExtSecret,
}

// seedRefusedDeviceWorld is subscription "s1" with a REALITY inbound the probes hide, a second
// inbound behind an external host, and an external link.
func seedRefusedDeviceWorld(t *testing.T) {
	t.Helper()
	w := newProbeSubWorld(t, service.SubProbeOrderHide)
	reality := `{"network":"tcp","security":"reality","realitySettings":{"serverNames":["reality.hidden.example.com"],"shortIds":["` +
		refusedShortID + `"],"settings":{"publicKey":"` + refusedRealityPBK + `","fingerprint":"chrome"}}}`
	db := database.GetDB()
	if err := db.Model(w.a).Update("stream_settings", reality).Error; err != nil {
		t.Fatal(err)
	}
	w.down(w.a, "203.0.113.5")
	seedHost(t, &model.Host{InboundId: w.b.Id, GroupId: "g1", SortOrder: 1, Remark: "front", Address: refusedHostAddr})
	var client model.ClientRecord
	if err := db.Where("email = ?", "b@e").First(&client).Error; err != nil {
		t.Fatal(err)
	}
	link := "trojan://" + refusedExtSecret + "@" + refusedExtServer + ":443?security=tls#external"
	if err := db.Create(&model.ClientExternalLink{ClientId: client.Id, Kind: model.ExternalLinkKindLink, Value: link}).Error; err != nil {
		t.Fatal(err)
	}
}

func renderForDevice(t *testing.T, format, notice string) string {
	t.Helper()
	const host = "req.example.com"
	var body string
	var err error
	switch format {
	case "raw":
		req := NewSubService("").ForRequest(host)
		req.subscriptionBody = true
		req.deviceNotice = notice
		var links []string
		links, _, _, _, err = req.getSubs("s1")
		body = strings.Join(links, "\n")
	case "json":
		body, _, err = NewSubJsonService("", "", "", "", NewSubService("")).getJson("s1", host, true, notice)
	case "clash", "clash-legacy":
		body, _, err = NewSubClashService(true, refusedClashRules, false, NewSubService("")).getClash("s1", host, format == "clash-legacy", notice)
	}
	if err != nil {
		t.Fatalf("%s: %v", format, err)
	}
	return body
}

// A refused device gets the notice and nothing of the client: no address, credential, REALITY
// key or short id, whatever probes, hosts, external links and a Clash template would add.
func TestRefusedDeviceAnswerCarriesNoClientSecret(t *testing.T) {
	for _, format := range []string{"raw", "json", "clash", "clash-legacy"} {
		t.Run(format, func(t *testing.T) {
			seedRefusedDeviceWorld(t)

			body := renderForDevice(t, format, refusedNotice)
			if !strings.Contains(strings.ReplaceAll(body, "%20", " "), refusedNotice) {
				t.Fatalf("refused answer lacks the notice:\n%s", body)
			}
			for _, secret := range refusedDeviceSecrets {
				if strings.Contains(body, secret) {
					t.Fatalf("refused answer leaks %q:\n%s", secret, body)
				}
			}
		})
	}
}

// The list above must be what an admitted device really gets, or the test above proves nothing.
func TestRefusedDeviceSecretsAreInTheServedSubscription(t *testing.T) {
	seedRefusedDeviceWorld(t)
	served := renderForDevice(t, "raw", "") + renderForDevice(t, "json", "") + renderForDevice(t, "clash", "")
	for _, secret := range refusedDeviceSecrets {
		if !strings.Contains(served, secret) {
			t.Fatalf("fixture secret %q is in no served format; the leak test cannot catch it", secret)
		}
	}
}
