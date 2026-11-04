package sub

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// The agent's job is built from the real subscription link of the probe
// client: it must carry that client's credentials and no other client's.
func TestReachProbeJobUsesSubscriptionLinkOfProbeClientOnly(t *testing.T) {
	seedSubDB(t)
	service.RegisterSubLinkProvider(NewLinkProvider())
	t.Cleanup(func() { service.RegisterSubLinkProvider(nil) })

	ib := &model.Inbound{
		UserId: 1, Tag: "probe-reality", Enable: true, Listen: "203.0.113.9", Port: 4443,
		Protocol: model.VLESS, Remark: "Main", Settings: `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"reality",
			"realitySettings":{"serverNames":["reality.example.com"],"shortIds":["ab12cd"],"settings":{"publicKey":"PBKvalue","fingerprint":"chrome"}}}`,
	}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	const userUUID = "99999999-8888-4777-8666-555555555555"
	clients := &service.ClientService{}
	inbounds := &service.InboundService{}
	if _, err := clients.Create(inbounds, &service.ClientCreatePayload{
		Client:     model.Client{Email: "real-user", ID: userUUID, Flow: "xtls-rprx-vision", Enable: true},
		InboundIds: []int{ib.Id},
	}); err != nil {
		t.Fatal(err)
	}

	probes := &service.ReachProbeService{}
	created, _, err := probes.Create(service.ReachProbeInput{Name: "home-1", InboundIds: []int{ib.Id}})
	if err != nil {
		t.Fatal(err)
	}
	row, err := probes.Authenticate(created.Token)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := clients.GetRecordByEmail(nil, created.Probe.ClientEmail)
	if err != nil {
		t.Fatal(err)
	}
	job, err := probes.BuildJob(row, "panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Targets) != 1 || job.Targets[0].Key != strconv.Itoa(ib.Id)+"@203.0.113.9:4443" || job.Targets[0].Label != "Main" {
		t.Fatalf("targets %+v", job.Targets)
	}
	raw := string(job.Targets[0].Outbound)
	if strings.Contains(raw, userUUID) || strings.Contains(raw, "real-user") {
		t.Fatalf("job leaks another client's credentials: %s", raw)
	}
	var ob struct {
		Protocol string `json:"protocol"`
		Settings struct {
			ID   string `json:"id"`
			Flow string `json:"flow"`
		} `json:"settings"`
		Stream struct {
			Security string `json:"security"`
			Reality  struct {
				ServerName string `json:"serverName"`
				Password   string `json:"password"`
				PublicKey  string `json:"publicKey"`
				ShortID    string `json:"shortId"`
			} `json:"realitySettings"`
		} `json:"streamSettings"`
	}
	if err := json.Unmarshal(job.Targets[0].Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	if ob.Protocol != "vless" || ob.Settings.ID != rec.UUID || ob.Settings.Flow != "xtls-rprx-vision" {
		t.Fatalf("outbound identity %+v", ob)
	}
	if ob.Stream.Security != "reality" || ob.Stream.Reality.ServerName != "reality.example.com" ||
		ob.Stream.Reality.ShortID != "ab12cd" || (ob.Stream.Reality.Password+ob.Stream.Reality.PublicKey) != "PBKvalue" {
		t.Fatalf("outbound reality %s", raw)
	}
}
