package service

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
)

// probeLinkProvider hands out the REALITY link a subscription would give the
// requested client, keyed by that client's stored UUID.
type probeLinkProvider struct{ fakeSubLinkProvider }

func (probeLinkProvider) LinksForClient(host string, inbound *model.Inbound, email string) []string {
	rec := &model.ClientRecord{}
	if err := database.GetDB().Where("email = ?", email).First(rec).Error; err != nil {
		return nil
	}
	return []string{"vless://" + rec.UUID + "@vpn.example.com:443?type=tcp&security=reality&pbk=PBK&sni=www.example.com&sid=ab&fp=chrome#" + inbound.Remark}
}

type probeFixture struct {
	svc    *ReachProbeService
	events []eventbus.Event
	now    time.Time
	inb    *model.Inbound
}

func newProbeFixture(t *testing.T) *probeFixture {
	t.Helper()
	setupBulkDB(t)
	RegisterSubLinkProvider(probeLinkProvider{})
	t.Cleanup(func() { RegisterSubLinkProvider(nil) })
	f := &probeFixture{now: time.UnixMilli(1_800_000_000_000)}
	f.svc = &ReachProbeService{
		now:     func() time.Time { return f.now },
		publish: func(e eventbus.Event) { f.events = append(f.events, e) },
	}
	f.inb = mkInbound(t, 24443, model.VLESS, `{"clients":[],"decryption":"none"}`)
	if err := database.GetDB().Model(f.inb).Update("remark", "VLESS-443").Error; err != nil {
		t.Fatal(err)
	}
	f.inb.Remark = "VLESS-443"
	return f
}

func (f *probeFixture) create(t *testing.T, name string) (*ReachProbeCreated, *model.ReachProbe) {
	t.Helper()
	created, _, err := f.svc.Create(ReachProbeInput{Name: name, InboundIds: []int{f.inb.Id}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	row, err := f.svc.Authenticate(created.Token)
	if err != nil {
		t.Fatalf("Authenticate fresh token: %v", err)
	}
	return created, row
}

func TestStepReachTargetNeedsTwoFailuresAndOneSuccess(t *testing.T) {
	st := &model.ReachProbeTarget{Status: ReachStatusUnknown}
	steps := []struct {
		ok     bool
		want   reachTransition
		status string
	}{
		{true, reachNoChange, ReachStatusOk},
		{false, reachNoChange, ReachStatusOk},
		{false, reachWentDown, ReachStatusFail},
		{false, reachNoChange, ReachStatusFail},
		{true, reachCameBack, ReachStatusOk},
		{true, reachNoChange, ReachStatusOk},
		{false, reachNoChange, ReachStatusOk},
		{true, reachNoChange, ReachStatusOk},
	}
	for i, s := range steps {
		if got := stepReachTarget(st, s.ok, int64(i)); got != s.want || st.Status != s.status {
			t.Fatalf("step %d (ok=%v): transition %v status %q, want %v %q", i, s.ok, got, st.Status, s.want, s.status)
		}
	}
	fresh := &model.ReachProbeTarget{Status: ReachStatusUnknown}
	stepReachTarget(fresh, false, 1)
	if got := stepReachTarget(fresh, false, 2); got != reachWentDown {
		t.Fatalf("a target that never worked must still be reported down after two failures, got %v", got)
	}
}

func TestReachProbeTokenIsHashedAndScopedToEnabledProbe(t *testing.T) {
	f := newProbeFixture(t)
	created, row := f.create(t, "home-1")
	if len(created.Token) != reachProbeTokenLength {
		t.Fatalf("token length %d", len(created.Token))
	}
	if row.TokenHash == created.Token || strings.Contains(row.TokenHash, created.Token) {
		t.Fatal("the plaintext token is stored")
	}
	if _, err := f.svc.Authenticate(created.Token + "x"); !errors.Is(err, ErrReachProbeUnauthorized) {
		t.Fatalf("wrong token: %v", err)
	}
	newToken, err := f.svc.RotateToken(row.Id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(created.Token); !errors.Is(err, ErrReachProbeUnauthorized) {
		t.Fatalf("rotated-out token still works: %v", err)
	}
	if _, err := f.svc.Authenticate(newToken); err != nil {
		t.Fatalf("rotated token: %v", err)
	}
	off := false
	in := ReachProbeInput{Name: "home-1", Enabled: &off, InboundIds: []int{f.inb.Id}}
	if _, err := f.svc.Update(row.Id, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(newToken); !errors.Is(err, ErrReachProbeUnauthorized) {
		t.Fatalf("a disabled probe's token still works: %v", err)
	}
}

func TestReachProbeCreateMakesAMarkedUnlimitedClient(t *testing.T) {
	f := newProbeFixture(t)
	created, _ := f.create(t, "home-1")
	rec := lookupClientRecord(t, created.Probe.ClientEmail)
	if !strings.HasPrefix(rec.Email, reachProbeClientPrefix) || !strings.Contains(rec.Comment, "home-1") {
		t.Fatalf("probe client is not marked: email %q comment %q", rec.Email, rec.Comment)
	}
	if rec.TotalGB != 0 || rec.ExpiryTime != 0 || rec.LimitIP != 0 || !rec.Enable {
		t.Fatalf("probe client is limited: %+v", rec)
	}
	if !settingsHoldUUID(t, &InboundService{}, f.inb.Id, rec.UUID) {
		t.Fatal("probe client was not attached to the inbound")
	}
	if _, err := f.svc.Delete(created.Probe.Id); err != nil {
		t.Fatal(err)
	}
	var left int64
	database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", rec.Email).Count(&left)
	if left != 0 {
		t.Fatal("deleting the probe left its client behind")
	}
}

func TestReachProbeRejectsWhatTheAgentCannotDo(t *testing.T) {
	f := newProbeFixture(t)
	wg := mkInbound(t, 24444, model.WireGuard, `{"peers":[]}`)
	cases := map[string]ReachProbeInput{
		"unsupported protocol": {Name: "a", InboundIds: []int{wg.Id}},
		"no inbound":           {Name: "a"},
		"missing inbound":      {Name: "a", InboundIds: []int{999}},
		"bad test url":         {Name: "a", InboundIds: []int{f.inb.Id}, TestUrl: "file:///etc/passwd"},
		"bad direct url":       {Name: "a", InboundIds: []int{f.inb.Id}, DirectUrl: "javascript:alert(1)"},
		"interval too short":   {Name: "a", InboundIds: []int{f.inb.Id}, IntervalSec: 5},
		"html in name":         {Name: "<b>x</b>", InboundIds: []int{f.inb.Id}},
		"empty name":           {Name: "  ", InboundIds: []int{f.inb.Id}},
	}
	for name, in := range cases {
		if _, _, err := f.svc.Create(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var n int64
	database.GetDB().Model(&model.ReachProbe{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d probes stored from rejected input", n)
	}
}

func TestReachProbeJobCarriesOnlyTheProbeClient(t *testing.T) {
	f := newProbeFixture(t)
	created, row := f.create(t, "home-1")
	rec := lookupClientRecord(t, created.Probe.ClientEmail)
	job, err := f.svc.BuildJob(row, "panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Targets) != 1 {
		t.Fatalf("targets: %+v", job.Targets)
	}
	tg := job.Targets[0]
	wantKey := strconv.Itoa(f.inb.Id) + "@vpn.example.com:443"
	if tg.Key != wantKey || tg.Label != "VLESS-443" || tg.InboundId != f.inb.Id || tg.Protocol != "vless" {
		t.Fatalf("target %+v", tg)
	}
	var ob map[string]any
	if err := json.Unmarshal(tg.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	settings := ob["settings"].(map[string]any)
	if settings["id"] != rec.UUID || ob["tag"] != "probe" {
		t.Fatalf("outbound %s", tg.Outbound)
	}
	if job.TestUrl != ReachProbeDefaultTestURL || job.IntervalSec != ReachProbeDefaultInterval {
		t.Fatalf("job defaults: %+v", job)
	}
}

func TestReachProbeIngestAlertsOnTransitionsOnly(t *testing.T) {
	f := newProbeFixture(t)
	_, row := f.create(t, "home-1")
	job, _ := f.svc.BuildJob(row, "panel.example.com")
	key := job.Targets[0].Key

	report := func(ok bool, errText string) {
		t.Helper()
		row, _ = f.svc.get(row.Id)
		f.now = f.now.Add(10 * time.Minute)
		n, err := f.svc.Ingest(row, "panel.example.com", &ReachReport{
			AgentVersion: "1.5.0",
			Results: []ReachReportResult{
				{Key: key, Ok: ok, Error: errText, TtfbMs: 120, TotalMs: 900, Bytes: 262144},
				{Key: "99@evil.example:443", Ok: false, Error: "forged"},
			},
		})
		if err != nil || n != 1 {
			t.Fatalf("Ingest: stored %d, err %v", n, err)
		}
	}
	report(true, "")
	report(false, "stalled after 16384 bytes:\n<b>timeout</b>")
	if len(f.events) != 0 {
		t.Fatalf("one failure alerted: %+v", f.events)
	}
	report(false, "timeout")
	if len(f.events) != 1 || f.events[0].Type != eventbus.EventProbeDown || f.events[0].Source != "home-1 · VLESS-443" {
		t.Fatalf("after two failures: %+v", f.events)
	}
	report(false, "timeout")
	if len(f.events) != 1 {
		t.Fatalf("a continuing outage re-alerted: %+v", f.events)
	}
	report(true, "")
	if len(f.events) != 2 || f.events[1].Type != eventbus.EventProbeUp {
		t.Fatalf("recovery: %+v", f.events)
	}

	var stored []model.ReachProbeResult
	database.GetDB().Where("probe_id = ?", row.Id).Order("id").Find(&stored)
	if len(stored) != 5 {
		t.Fatalf("stored %d results, want 5 (forged key dropped)", len(stored))
	}
	if got := stored[1].ErrorText; got != "stalled after 16384 bytes: <b>timeout</b>" {
		t.Fatalf("error text kept a line break: %q", got)
	}
	views, err := f.svc.List()
	if err != nil {
		t.Fatal(err)
	}
	tv := views[0].Targets[0]
	if tv.Status != ReachStatusOk || !tv.LastOk || tv.Checks7d != 5 || tv.Uptime7d != 40 || views[0].AgentVersion != "1.5.0" {
		t.Fatalf("view %+v / %+v", views[0], tv)
	}
}

func TestReachProbeIngestRefusesOversizedReports(t *testing.T) {
	f := newProbeFixture(t)
	_, row := f.create(t, "home-1")
	results := make([]ReachReportResult, reachProbeMaxTargets+1)
	if _, err := f.svc.Ingest(row, "panel.example.com", &ReachReport{Results: results}); !errors.Is(err, ErrReachReportTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestReachProbeIngestPrunesOldHistory(t *testing.T) {
	f := newProbeFixture(t)
	_, row := f.create(t, "home-1")
	old := model.ReachProbeResult{ProbeId: row.Id, TargetKey: "x", At: f.now.Add(-8 * 24 * time.Hour).UnixMilli()}
	database.GetDB().Create(&old)
	if _, err := f.svc.Ingest(row, "panel.example.com", &ReachReport{}); err != nil {
		t.Fatal(err)
	}
	var n int64
	database.GetDB().Model(&model.ReachProbeResult{}).Where("id = ?", old.Id).Count(&n)
	if n != 0 {
		t.Fatal("a result older than seven days survived a report")
	}
}

func TestReachProbeStaleAlertsOnceAndAnnouncesReturn(t *testing.T) {
	f := newProbeFixture(t)
	_, row := f.create(t, "home-1")
	if err := f.svc.CheckStale(); err != nil || len(f.events) != 0 {
		t.Fatalf("a probe that never reported was called stale: %v %+v", err, f.events)
	}
	if _, err := f.svc.Ingest(row, "panel.example.com", &ReachReport{}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(29 * time.Minute)
	_ = f.svc.CheckStale()
	if len(f.events) != 0 {
		t.Fatalf("stale before three intervals: %+v", f.events)
	}
	f.now = f.now.Add(2 * time.Minute)
	_ = f.svc.CheckStale()
	_ = f.svc.CheckStale()
	if len(f.events) != 1 || f.events[0].Type != eventbus.EventProbeStale {
		t.Fatalf("stale events: %+v", f.events)
	}
	if d := f.events[0].Data.(*eventbus.ProbeHealthData); d.SilentFor != 31 {
		t.Fatalf("silent for %d min", d.SilentFor)
	}
	row, _ = f.svc.get(row.Id)
	if _, err := f.svc.Ingest(row, "panel.example.com", &ReachReport{}); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 2 || f.events[1].Type != eventbus.EventProbeUp || f.events[1].Data.(*eventbus.ProbeHealthData).Target != "" {
		t.Fatalf("return of a stale probe: %+v", f.events)
	}
}

func TestReachProbeUpdateMovesTheClient(t *testing.T) {
	f := newProbeFixture(t)
	created, row := f.create(t, "home-1")
	other := mkInbound(t, 24445, model.Trojan, `{"clients":[]}`)
	in := ReachProbeInput{Name: "home-2", InboundIds: []int{other.Id}, IntervalSec: 300}
	if _, err := f.svc.Update(row.Id, in); err != nil {
		t.Fatal(err)
	}
	rec := lookupClientRecord(t, created.Probe.ClientEmail)
	ids, _ := (&ClientService{}).GetInboundIdsForRecord(rec.Id)
	if len(ids) != 1 || ids[0] != other.Id {
		t.Fatalf("client attached to %v, want only %d", ids, other.Id)
	}
	row, _ = f.svc.get(row.Id)
	if row.Name != "home-2" || row.IntervalSec != 300 || len(row.InboundIds) != 1 || row.InboundIds[0] != other.Id || !row.Enabled {
		t.Fatalf("row %+v", row)
	}
}
