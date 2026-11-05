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
	created, _, err := f.svc.Create(ReachProbeInput{Name: name, InboundIds: []int{f.inb.Id}}, "panel.example.com")
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
	newToken, _, err := f.svc.RotateToken(row.Id)
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
	if _, err := f.svc.Update(row.Id, in, "panel.example.com"); err != nil {
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

func countProbeClientRecords(t *testing.T, email string) int64 {
	t.Helper()
	var n int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", email).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReachProbeRotateAndDisableRevokeVPNAccess(t *testing.T) {
	f := newProbeFixture(t)
	created, row := f.create(t, "home-1")
	email := created.Probe.ClientEmail
	first := lookupClientRecord(t, email)

	if _, _, err := f.svc.RotateToken(row.Id); err != nil {
		t.Fatal(err)
	}
	second := lookupClientRecord(t, email)
	if second.UUID == first.UUID || second.SubID == first.SubID {
		t.Fatalf("rotation kept the old credentials: uuid %q sub %q", second.UUID, second.SubID)
	}
	if settingsHoldUUID(t, &InboundService{}, f.inb.Id, first.UUID) {
		t.Fatal("the rotated-out UUID is still in the inbound")
	}
	if !settingsHoldUUID(t, &InboundService{}, f.inb.Id, second.UUID) {
		t.Fatal("the new probe client is not in the inbound")
	}

	off, on := false, true
	if _, err := f.svc.Update(row.Id, ReachProbeInput{Name: "home-1", Enabled: &off, InboundIds: []int{f.inb.Id}}, "panel.example.com"); err != nil {
		t.Fatal(err)
	}
	if countProbeClientRecords(t, email) != 0 || settingsHoldUUID(t, &InboundService{}, f.inb.Id, second.UUID) {
		t.Fatal("a disabled probe still has a working client")
	}
	if _, _, err := f.svc.RotateToken(row.Id); err != nil {
		t.Fatalf("rotating a disabled probe: %v", err)
	}
	if countProbeClientRecords(t, email) != 0 {
		t.Fatal("rotating a disabled probe brought its client back")
	}

	if _, err := f.svc.Update(row.Id, ReachProbeInput{Name: "home-1", Enabled: &on, InboundIds: []int{f.inb.Id}}, "panel.example.com"); err != nil {
		t.Fatal(err)
	}
	third := lookupClientRecord(t, email)
	if !third.Enable || third.UUID == second.UUID || !settingsHoldUUID(t, &InboundService{}, f.inb.Id, third.UUID) {
		t.Fatalf("re-enabled probe client: %+v", third)
	}

	if _, err := f.svc.Delete(row.Id); err != nil {
		t.Fatal(err)
	}
	if countProbeClientRecords(t, email) != 0 || settingsHoldUUID(t, &InboundService{}, f.inb.Id, third.UUID) {
		t.Fatal("deleting the probe left its client behind")
	}
}

func TestReachProbeToleratesAClientDeletedByHand(t *testing.T) {
	f := newProbeFixture(t)
	created, row := f.create(t, "home-1")
	if _, err := (&ClientService{}).DeleteByEmail(&InboundService{}, created.Probe.ClientEmail, false); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := f.svc.Update(row.Id, ReachProbeInput{Name: "home-1", Enabled: &off, InboundIds: []int{f.inb.Id}}, "panel.example.com"); err != nil {
		t.Fatalf("disabling a probe whose client is gone: %v", err)
	}
	if _, err := f.svc.Delete(row.Id); err != nil {
		t.Fatalf("deleting a probe whose client is gone: %v", err)
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
		if _, _, err := f.svc.Create(in, "panel.example.com"); err == nil {
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
	job, err := f.svc.BuildJob(row)
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
	job, _ := f.svc.BuildJob(row)
	key := job.Targets[0].Key

	report := func(ok bool, errText string) {
		t.Helper()
		row, _ = f.svc.get(row.Id)
		f.now = f.now.Add(10 * time.Minute)
		n, err := f.svc.Ingest(row, &ReachReport{
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
	if _, err := f.svc.Ingest(row, &ReachReport{Results: results}); !errors.Is(err, ErrReachReportTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestReachProbeIngestPrunesOldHistory(t *testing.T) {
	f := newProbeFixture(t)
	_, row := f.create(t, "home-1")
	old := model.ReachProbeResult{ProbeId: row.Id, TargetKey: "x", At: f.now.Add(-8 * 24 * time.Hour).UnixMilli()}
	database.GetDB().Create(&old)
	if _, err := f.svc.Ingest(row, &ReachReport{}); err != nil {
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
	if _, err := f.svc.Ingest(row, &ReachReport{}); err != nil {
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
	if _, err := f.svc.Ingest(row, &ReachReport{}); err != nil {
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
	if _, err := f.svc.Update(row.Id, in, "panel.example.com"); err != nil {
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

// hostLinkProvider builds the probe client's link for the host it is asked for, as the real
// subscription builder does when no inbound address is set.
type hostLinkProvider struct{ fakeSubLinkProvider }

func (hostLinkProvider) LinksForClient(host string, inbound *model.Inbound, email string) []string {
	rec := &model.ClientRecord{}
	if err := database.GetDB().Where("email = ?", email).First(rec).Error; err != nil {
		return nil
	}
	return []string{"vless://" + rec.UUID + "@" + host + ":443?type=tcp&security=none#" + inbound.Remark}
}

func jobKeys(t *testing.T, f *probeFixture, id int) []string {
	t.Helper()
	row, err := f.svc.get(id)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.BuildJob(row)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, tg := range job.Targets {
		keys = append(keys, tg.Key)
	}
	return keys
}

// STILL-PROBE-1: the job is built for the address the admin saved the probe from; a loopback
// address (an SSH tunnel) does not replace it, a real one does.
func TestReachProbeJobUsesTheSavedPanelHost(t *testing.T) {
	f := newProbeFixture(t)
	RegisterSubLinkProvider(hostLinkProvider{})
	_, row := f.create(t, "home-1")
	want := strconv.Itoa(f.inb.Id) + "@panel.example.com:443"
	if keys := jobKeys(t, f, row.Id); len(keys) != 1 || keys[0] != want {
		t.Fatalf("job keys %v, want [%s]", keys, want)
	}
	in := ReachProbeInput{Name: "home-1", InboundIds: []int{f.inb.Id}}
	for _, tunnel := range []string{"127.0.0.1", "localhost", "::1", "0.0.0.0", "", "bad host!"} {
		if _, err := f.svc.Update(row.Id, in, tunnel); err != nil {
			t.Fatal(err)
		}
		if keys := jobKeys(t, f, row.Id); keys[0] != want {
			t.Fatalf("saving from %q moved the job to %v", tunnel, keys)
		}
	}
	if _, err := f.svc.Update(row.Id, in, "vpn2.example.com"); err != nil {
		t.Fatal(err)
	}
	if keys := jobKeys(t, f, row.Id); keys[0] != strconv.Itoa(f.inb.Id)+"@vpn2.example.com:443" {
		t.Fatalf("a real address did not replace the saved one: %v", keys)
	}
	// A configured public host wins, as in subscriptions.
	setSetting(t, "webDomain", "cfg.example.com")
	if keys := jobKeys(t, f, row.Id); keys[0] != strconv.Itoa(f.inb.Id)+"@cfg.example.com:443" {
		t.Fatalf("the web domain is not used: %v", keys)
	}
}

// A probe without any address refuses jobs and reports with a clear error instead of trusting the
// agent; the Probes page gives it the admin's address.
func TestReachProbeWithoutAddressRefusesUntilAdopted(t *testing.T) {
	f := newProbeFixture(t)
	RegisterSubLinkProvider(hostLinkProvider{})
	created, _, err := f.svc.Create(ReachProbeInput{Name: "old", InboundIds: []int{f.inb.Id}}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	row, _ := f.svc.Authenticate(created.Token)
	if _, err := f.svc.BuildJob(row); !errors.Is(err, ErrReachProbeNoHost) {
		t.Fatalf("BuildJob = %v, want ErrReachProbeNoHost", err)
	}
	if _, err := f.svc.Ingest(row, &ReachReport{}); !errors.Is(err, ErrReachProbeNoHost) {
		t.Fatalf("Ingest = %v, want ErrReachProbeNoHost", err)
	}
	if err := f.svc.AdoptPanelHost("localhost"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AdoptPanelHost("panel.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AdoptPanelHost("other.example.com"); err != nil {
		t.Fatal(err)
	}
	if keys := jobKeys(t, f, row.Id); keys[0] != strconv.Itoa(f.inb.Id)+"@panel.example.com:443" {
		t.Fatalf("adopted host %v, want the first real one only", keys)
	}
}

// However many keys a token invents, a probe keeps at most reachProbeMaxTargets alert states, and
// the ones of its current job survive.
func TestReachProbeTargetsStayCapped(t *testing.T) {
	f := newProbeFixture(t)
	_, row := f.create(t, "home-1")
	for i := range reachProbeMaxTargets + 10 {
		st := model.ReachProbeTarget{ProbeId: row.Id, TargetKey: strconv.Itoa(f.inb.Id) + "@x" + strconv.Itoa(i) + ".example:443", Status: ReachStatusOk}
		if err := database.GetDB().Create(&st).Error; err != nil {
			t.Fatal(err)
		}
	}
	key := strconv.Itoa(f.inb.Id) + "@vpn.example.com:443"
	if _, err := f.svc.Ingest(row, &ReachReport{Results: []ReachReportResult{{Key: key, Ok: true}}}); err != nil {
		t.Fatal(err)
	}
	var n, kept int64
	database.GetDB().Model(&model.ReachProbeTarget{}).Where("probe_id = ?", row.Id).Count(&n)
	database.GetDB().Model(&model.ReachProbeTarget{}).Where("probe_id = ? AND target_key = ?", row.Id, key).Count(&kept)
	if n > reachProbeMaxTargets || kept != 1 {
		t.Fatalf("%d targets (cap %d), current target kept %d", n, reachProbeMaxTargets, kept)
	}
}

// The hourly cleanup drops results older than the history of probes that went silent too, and
// the targets nothing has checked within it.
func TestReachProbePruneHistoryDropsOldRowsAndOrphanTargets(t *testing.T) {
	f := newProbeFixture(t)
	_, row := f.create(t, "home-1")
	db := database.GetDB()
	old := f.now.Add(-8 * 24 * time.Hour).UnixMilli()
	recent := f.now.Add(-time.Hour).UnixMilli()
	for _, r := range []model.ReachProbeResult{
		{ProbeId: row.Id, TargetKey: "orphan", At: old},
		{ProbeId: row.Id, TargetKey: "live", At: old},
		{ProbeId: row.Id, TargetKey: "live", At: recent},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"orphan", "live"} {
		if err := db.Create(&model.ReachProbeTarget{ProbeId: row.Id, TargetKey: key, Status: ReachStatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.PruneHistory(); err != nil {
		t.Fatal(err)
	}
	var results, oldLeft, targets int64
	db.Model(&model.ReachProbeResult{}).Count(&results)
	db.Model(&model.ReachProbeResult{}).Where("at < ?", f.now.Add(-reachProbeHistory).UnixMilli()).Count(&oldLeft)
	db.Model(&model.ReachProbeTarget{}).Where("probe_id = ?", row.Id).Count(&targets)
	var live int64
	db.Model(&model.ReachProbeTarget{}).Where("target_key = ?", "live").Count(&live)
	if results != 1 || oldLeft != 0 || targets != 1 || live != 1 {
		t.Fatalf("after cleanup: %d results (%d old), %d targets (live %d); want 1, 0, 1, 1", results, oldLeft, targets, live)
	}
}

// Xray keeps the VMess endpoint in settings.vnext and the Trojan/Shadowsocks one in
// settings.servers; reading only the flat form gave those inbounds no probe target at all.
func TestReachProbeJobTargetsEveryProtocolShape(t *testing.T) {
	cases := []struct{ name, link, key string }{
		{"vmess", "vmess://eyJ2IjoiMiIsInBzIjoiYSIsImFkZCI6InZtLmV4YW1wbGUuY29tIiwicG9ydCI6IjgwODAiLCJpZCI6IjExMTExMTExLTIyMjItNDMzMy04NDQ0LTU1NTU1NTU1NTU1NSIsIm5ldCI6InRjcCJ9", "vm.example.com:8080"},
		{"trojan", "trojan://secret@tr.example.com:8443?security=tls#a", "tr.example.com:8443"},
		{"shadowsocks", "ss://YWVzLTI1Ni1nY206c2VjcmV0@ss.example.com:8388#a", "ss.example.com:8388"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ib := &model.Inbound{Id: 7, Remark: "X", Protocol: model.Trojan}
			target, ok := reachJobTarget(ib, tc.link, false)
			if !ok || target.Key != "7@"+tc.key {
				t.Fatalf("target %+v ok=%v, want key 7@%s", target, ok, tc.key)
			}
		})
	}
}
