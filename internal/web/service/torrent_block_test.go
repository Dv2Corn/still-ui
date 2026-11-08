package service

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/nfttable"
	"github.com/mhsanaei/3x-ui/v3/internal/torrentblock"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type fakeHostFilter struct {
	protected              []uint16
	applyErr               error
	applied, ensured, gone int
}

func (f *fakeHostFilter) Apply(p []uint16) error {
	f.applied++
	f.protected = p
	return f.applyErr
}

func (f *fakeHostFilter) Ensure(p []uint16) error {
	f.ensured++
	f.protected = p
	return f.applyErr
}
func (f *fakeHostFilter) Remove() error { f.gone++; return nil }
func (f *fakeHostFilter) Status() torrentblock.Status {
	return torrentblock.Status{Supported: true, NftInstalled: true, Active: f.applied > f.gone}
}

func useFakeHostFilter(t *testing.T) *fakeHostFilter {
	t.Helper()
	fake := &fakeHostFilter{}
	prev := torrentBlockHost
	torrentBlockHost = fake
	t.Cleanup(func() {
		torrentBlockHost = prev
		torrentBlockErr = ""
	})
	return fake
}

const tbVlessStream = `{"network":"tcp","security":"none","tcpSettings":{"header":{"type":"none"}}}`

func addTorrentBlockInbound(t *testing.T, tag string, port int, sniffing string) {
	t.Helper()
	in := &model.Inbound{
		Tag: tag, Enable: true, Listen: "127.0.0.1", Port: port, Protocol: model.VLESS,
		Settings:       `{"clients":[{"id":"8b6f1a1e-6d39-4b0c-9a5e-3c1f0d1c2b3a","email":"` + tag + `@example.test","enable":true}],"decryption":"none"}`,
		StreamSettings: tbVlessStream,
		Sniffing:       sniffing,
	}
	if _, _, err := (&InboundService{}).AddInbound(in); err != nil {
		t.Fatalf("AddInbound %s: %v", tag, err)
	}
}

func emittedInbound(t *testing.T, cfg *xray.Config, tag string) map[string]any {
	t.Helper()
	for i := range cfg.InboundConfigs {
		if cfg.InboundConfigs[i].Tag != tag {
			continue
		}
		raw, _ := json.Marshal(cfg.InboundConfigs[i])
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}
	t.Fatalf("inbound %s missing from the generated config", tag)
	return nil
}

func firstRule(t *testing.T, cfg *xray.Config) map[string]any {
	t.Helper()
	var routing struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil || len(routing.Rules) == 0 {
		t.Fatalf("routing %s: %v", cfg.RouterConfig, err)
	}
	return routing.Rules[0]
}

// Xray only tags a connection "bittorrent" when its inbound sniffs content, so the
// toggle must add the rule first and switch sniffing on where the operator left it off.
func TestGetXrayConfig_TorrentBlockRuleAndSniffing(t *testing.T) {
	setupConflictDB(t)
	useFakeHostFilter(t)
	addTorrentBlockInbound(t, "in-unsniffed", 30001, `{"enabled":false,"destOverride":["http","tls"]}`)
	sniffing := `{"enabled":true,"destOverride":["http","tls","quic"],"routeOnly":false}`
	addTorrentBlockInbound(t, "in-sniffed", 30002, sniffing)

	off, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	if rule := firstRule(t, off); reflect.DeepEqual(rule["protocol"], []any{"bittorrent"}) {
		t.Fatalf("toggle off but first rule is %v", rule)
	}
	if s := emittedInbound(t, off, "in-unsniffed")["sniffing"].(map[string]any); s["enabled"] != false {
		t.Fatalf("toggle off changed sniffing to %v", s)
	}

	if _, err := (&TorrentBlockService{}).SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	on, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	want := map[string]any{"type": "field", "protocol": []any{"bittorrent"}, "outboundTag": "blocked"}
	if rule := firstRule(t, on); !reflect.DeepEqual(rule, want) {
		t.Fatalf("first rule = %v, want %v", rule, want)
	}
	assertXrayAccepts(t, "the torrent rule", buildGoldenRouting(map[string]any{"rules": []any{want}}))

	unsniffed := emittedInbound(t, on, "in-unsniffed")
	wantSniff := map[string]any{"enabled": true, "destOverride": []any{}, "routeOnly": true, "metadataOnly": false}
	if got := unsniffed["sniffing"]; !reflect.DeepEqual(got, wantSniff) {
		t.Fatalf("sniffing = %v, want %v (content sniffing, destinations untouched)", got, wantSniff)
	}
	assertXrayAccepts(t, "the inbound with forced sniffing", buildGoldenInbound(t, unsniffed))

	for i := range on.InboundConfigs {
		if on.InboundConfigs[i].Tag == "in-sniffed" && string(on.InboundConfigs[i].Sniffing) != sniffing {
			t.Fatalf("an inbound already sniffing content was rewritten to %s", on.InboundConfigs[i].Sniffing)
		}
	}
}

func TestInjectTorrentBlock_AddsBlackholeWhenTemplateHasNone(t *testing.T) {
	cfg := &xray.Config{
		RouterConfig:    json_util.RawMessage(`{"rules":[{"type":"field","outboundTag":"direct","network":"tcp,udp"}]}`),
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"},{"tag":"torrent-block","protocol":"freedom"}]`),
	}
	injectTorrentBlock(cfg, nil)
	var outbounds []map[string]any
	_ = json.Unmarshal(cfg.OutboundConfigs, &outbounds)
	last := outbounds[len(outbounds)-1]
	if len(outbounds) != 3 || last["tag"] != "torrent-block-2" || last["protocol"] != "blackhole" {
		t.Fatalf("outbounds = %v, want a new blackhole that does not reuse a taken tag", outbounds)
	}
	if rule := firstRule(t, cfg); rule["outboundTag"] != "torrent-block-2" {
		t.Fatalf("first rule = %v, want it to target the new blackhole", rule)
	}
}

// A host without nft still gets the Xray half: the toggle stays on and the error is shown.
func TestTorrentBlockService_HostErrorsDoNotBlockTheToggle(t *testing.T) {
	setupConflictDB(t)
	fake := useFakeHostFilter(t)
	svc := &TorrentBlockService{}

	svc.EnsureHostFilter()
	if fake.ensured != 0 {
		t.Fatal("EnsureHostFilter touched nftables while the toggle is off")
	}

	fake.applyErr = nfttable.ErrNftMissing
	st, err := svc.SetEnabled(true)
	if err != nil {
		t.Fatalf("SetEnabled(true) = %v", err)
	}
	if !st.Enabled || !svc.Enabled() || st.Error != nfttable.ErrNftMissing.Error() {
		t.Fatalf("status = %+v, want enabled with the nft error", st)
	}

	fake.applyErr = nil
	svc.EnsureHostFilter()
	if fake.ensured != 1 || svc.Status().Error != "" {
		t.Fatalf("EnsureHostFilter: ensured %d, error %q; want a retry that clears the error", fake.ensured, svc.Status().Error)
	}

	st, err = svc.SetEnabled(false)
	if err != nil || st.Enabled || fake.gone != 1 || st.Active {
		t.Fatalf("SetEnabled(false) = %+v, %v; removed %d times", st, err, fake.gone)
	}
}

// A node panel or an inbound answering with a BitTorrent-looking payload must never be
// learned as a swarm peer, so their ports reach the nftables protected set.
func TestTorrentBlockService_ProtectsOwnAndNodePorts(t *testing.T) {
	setupConflictDB(t)
	fake := useFakeHostFilter(t)
	addTorrentBlockInbound(t, "in-guarded", 30011, `{"enabled":true,"destOverride":["http"]}`)
	if err := (&SettingService{}).setString("webPort", "54321"); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.Node{Name: "n1", Address: "203.0.113.5", Port: 41000}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	if _, err := (&TorrentBlockService{}).SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	for _, want := range []uint16{54321, 30011, 41000} {
		if !slices.Contains(fake.protected, want) {
			t.Fatalf("protected ports %v miss %d", fake.protected, want)
		}
	}
}

// With XUI_PORT set the panel listens there, not on the stored webPort.
func TestTorrentBlockService_ProtectsThePortXUIPortSets(t *testing.T) {
	setupConflictDB(t)
	fake := useFakeHostFilter(t)
	t.Setenv("XUI_PORT", "8443")
	if _, err := (&TorrentBlockService{}).SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if !slices.Contains(fake.protected, 8443) {
		t.Fatalf("protected ports %v miss 8443, the port the panel listens on", fake.protected)
	}
}
