package service

import (
	"strconv"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

// subProbeWorld writes probe rows straight into the tables Ingest maintains, so each test states
// the exact alert state and check times it is about.
type subProbeWorld struct {
	t    *testing.T
	now  time.Time
	svc  *ReachProbeService
	next int
}

func newSubProbeWorld(t *testing.T) *subProbeWorld {
	t.Helper()
	setupBulkDB(t)
	w := &subProbeWorld{t: t, now: time.UnixMilli(1_800_000_000_000)}
	w.svc = &ReachProbeService{now: func() time.Time { return w.now }}
	return w
}

// agent adds an enabled probe of the inbounds that last reported silentFor ago.
func (w *subProbeWorld) agent(silentFor time.Duration, inbounds ...int) *model.ReachProbe {
	w.t.Helper()
	w.next++
	row := &model.ReachProbe{
		Name: "agent-" + strconv.Itoa(w.next), TokenHash: "hash-" + strconv.Itoa(w.next), Enabled: true,
		ClientEmail: "still-probe-" + strconv.Itoa(w.next), InboundIds: inbounds, TestURL: ReachProbeDefaultTestURL,
		IntervalSec: 600, LastSeenAt: w.now.Add(-silentFor).UnixMilli(),
	}
	if err := database.GetDB().Create(row).Error; err != nil {
		w.t.Fatal(err)
	}
	return row
}

// check records the probe's alert state of key and its last check checkedAgo ago.
func (w *subProbeWorld) check(probe *model.ReachProbe, key string, consecutiveFails int, checkedAgo time.Duration) {
	w.t.Helper()
	status := ReachStatusOk
	if consecutiveFails >= ReachProbeFailThreshold {
		status = ReachStatusFail
	}
	db := database.GetDB()
	if err := db.Create(&model.ReachProbeTarget{ProbeId: probe.Id, TargetKey: key, Status: status, ConsecutiveFails: consecutiveFails}).Error; err != nil {
		w.t.Fatal(err)
	}
	id, _ := reachTargetInbound(key)
	if err := db.Create(&model.ReachProbeResult{
		ProbeId: probe.Id, TargetKey: key, InboundId: id, Ok: consecutiveFails == 0, At: w.now.Add(-checkedAgo).UnixMilli(),
	}).Error; err != nil {
		w.t.Fatal(err)
	}
}

func (w *subProbeWorld) verdicts(policy ReachSubPolicy) *ReachSubVerdicts {
	w.t.Helper()
	if policy.StaleMinutes == 0 {
		policy.StaleMinutes = SubProbeStaleMinutesDefault
	}
	v, err := w.svc.SubscriptionVerdicts(policy)
	if err != nil {
		w.t.Fatal(err)
	}
	return v
}

var reorder = ReachSubPolicy{Mode: SubProbeOrderReorder}

func TestSubVerdictNeedsTheFailThresholdInARow(t *testing.T) {
	w := newSubProbeWorld(t)
	a := w.agent(time.Minute, 1, 2)
	w.check(a, "1@vpn.example.com:443", ReachProbeFailThreshold, time.Minute)
	w.check(a, "2@vpn.example.com:8443", ReachProbeFailThreshold-1, time.Minute)
	v := w.verdicts(reorder)
	if !v.Unreachable(1, "vpn.example.com", 443) {
		t.Fatal("a target failing the threshold in a row must count as unreachable")
	}
	if v.Unreachable(2, "vpn.example.com", 8443) {
		t.Fatal("one failed check is noise, not an outage")
	}
}

func TestSubVerdictWithoutDataIsReachable(t *testing.T) {
	w := newSubProbeWorld(t)
	a := w.agent(time.Minute, 1, 2)
	w.check(a, "1@vpn.example.com:443", ReachProbeFailThreshold, time.Minute)
	w.check(a, "2@vpn.example.com:8443", 0, time.Minute)
	v := w.verdicts(reorder)
	for _, c := range []struct {
		id   int
		host string
		port int
	}{{1, "other.example.com", 443}, {1, "vpn.example.com", 444}, {3, "vpn.example.com", 443}} {
		if v.Unreachable(c.id, c.host, c.port) {
			t.Fatalf("endpoint %d@%s:%d has no probe data and must stay reachable", c.id, c.host, c.port)
		}
	}
	if !v.Unreachable(1, "VPN.Example.com", 443) {
		t.Fatal("the host must match case-insensitively, as DNS does")
	}
}

// A silent agent says nothing about the endpoint: neither down nor confirmation of down.
func TestSubVerdictIgnoresSilentAgentsAndOldChecks(t *testing.T) {
	w := newSubProbeWorld(t)
	silent := w.agent(31*time.Minute, 1, 2)
	w.check(silent, "1@vpn.example.com:443", 5, 31*time.Minute)
	w.check(silent, "2@vpn.example.com:8443", 0, 31*time.Minute)
	fresh := w.agent(time.Minute, 3, 4)
	w.check(fresh, "3@vpn.example.com:9443", 3, 40*time.Minute) // its own last check is old
	w.check(fresh, "4@vpn.example.com:7443", 0, time.Minute)
	v := w.verdicts(reorder)
	if v.Unreachable(1, "vpn.example.com", 443) {
		t.Fatal("results of an agent silent past the stale window must be ignored")
	}
	if v.Unreachable(3, "vpn.example.com", 9443) {
		t.Fatal("a target not checked within the stale window must be ignored")
	}
	if v.ignored[silent.Id] != ReachSubIgnoredSilent {
		t.Fatalf("silent agent reason = %q", v.ignored[silent.Id])
	}
	longer := w.verdicts(ReachSubPolicy{Mode: SubProbeOrderReorder, StaleMinutes: 60})
	if !longer.Unreachable(1, "vpn.example.com", 443) || !longer.Unreachable(3, "vpn.example.com", 9443) {
		t.Fatal("a longer stale window must take the same results into account")
	}
}

func TestSubVerdictAutoMinAgents(t *testing.T) {
	w := newSubProbeWorld(t)
	home := w.agent(time.Minute, 1, 2)
	mobile := w.agent(time.Minute, 1, 2)
	silent := w.agent(time.Hour, 3, 4)
	lone := w.agent(time.Minute, 3, 4)
	w.check(home, "1@vpn.example.com:443", 2, time.Minute)
	w.check(home, "2@vpn.example.com:8443", 0, time.Minute)
	w.check(mobile, "1@vpn.example.com:443", 0, time.Minute)
	w.check(mobile, "2@vpn.example.com:8443", 0, time.Minute)
	w.check(lone, "3@vpn.example.com:9443", 2, time.Minute)
	w.check(lone, "4@vpn.example.com:7443", 0, time.Minute)
	only := w.agent(time.Minute, 5, 6)
	w.check(only, "5@vpn.example.com:6443", 2, time.Minute)
	w.check(only, "6@vpn.example.com:5443", 0, time.Minute)
	_ = silent

	v := w.verdicts(reorder)
	if v.Unreachable(1, "vpn.example.com", 443) {
		t.Fatal("with two agents on the inbound, one agent's failure must not be enough")
	}
	if got := v.byKey["1@vpn.example.com:443"]; got.FailAgents != 1 || got.NeedAgents != 2 {
		t.Fatalf("verdict = %+v, want 1 of 2", got)
	}
	if v.Unreachable(3, "vpn.example.com", 9443) {
		t.Fatal("an agent that went silent must not lower the number of agents needed")
	}
	if !v.Unreachable(5, "vpn.example.com", 6443) {
		t.Fatal("an inbound only one agent checks is decided by that agent")
	}

	if err := database.GetDB().Model(&model.ReachProbeTarget{}).
		Where("probe_id = ? AND target_key = ?", mobile.Id, "1@vpn.example.com:443").
		Updates(map[string]any{"status": ReachStatusFail, "consecutive_fails": 2}).Error; err != nil {
		t.Fatal(err)
	}
	if !w.verdicts(reorder).Unreachable(1, "vpn.example.com", 443) {
		t.Fatal("two agents agreeing must confirm the outage")
	}
	if w.verdicts(ReachSubPolicy{Mode: SubProbeOrderReorder, MinAgents: 3}).Unreachable(1, "vpn.example.com", 443) {
		t.Fatal("an explicit minimum of three agents must not be met by two")
	}
	if !w.verdicts(ReachSubPolicy{Mode: SubProbeOrderReorder, MinAgents: 1}).Unreachable(3, "vpn.example.com", 9443) {
		t.Fatal("an explicit minimum of one agent must be met by one")
	}
}

// Every target of one agent failing at once points at the agent's network or its test URL.
func TestSubVerdictIgnoresAnAgentThatFailsEverything(t *testing.T) {
	w := newSubProbeWorld(t)
	a := w.agent(time.Minute, 1, 2)
	w.check(a, "1@vpn.example.com:443", 2, time.Minute)
	w.check(a, "2@vpn.example.com:8443", 2, time.Minute)
	w.check(a, ReachDirectKey, 0, time.Minute)
	v := w.verdicts(reorder)
	if v.Unreachable(1, "vpn.example.com", 443) || v.Unreachable(2, "vpn.example.com", 8443) {
		t.Fatal("an agent failing on every target must be ignored")
	}
	if v.ignored[a.Id] != ReachSubIgnoredAllFailing {
		t.Fatalf("reason = %q", v.ignored[a.Id])
	}
}

func TestSubVerdictOffOrDisabledDecidesNothing(t *testing.T) {
	w := newSubProbeWorld(t)
	a := w.agent(time.Minute, 1, 2)
	w.check(a, "1@vpn.example.com:443", 2, time.Minute)
	w.check(a, "2@vpn.example.com:8443", 0, time.Minute)
	if w.verdicts(ReachSubPolicy{Mode: SubProbeOrderOff}).Unreachable(1, "vpn.example.com", 443) {
		t.Fatal("mode off must decide nothing")
	}
	if err := database.GetDB().Model(a).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if w.verdicts(reorder).Unreachable(1, "vpn.example.com", 443) {
		t.Fatal("a disabled probe must not count")
	}
}

func TestSubVerdictMatchesIPv6Endpoints(t *testing.T) {
	w := newSubProbeWorld(t)
	a := w.agent(time.Minute, 1, 2)
	w.check(a, "1@[2001:DB8::1]:443", 2, time.Minute)
	w.check(a, "2@vpn.example.com:8443", 0, time.Minute)
	v := w.verdicts(reorder)
	if !v.Unreachable(1, "2001:db8::1", 443) || !v.Unreachable(1, "[2001:db8::1]", 443) {
		t.Fatal("a bracketed IPv6 target must match the bare and bracketed link host")
	}
}

func TestSubProbeSettingsValidation(t *testing.T) {
	cases := []struct {
		name               string
		order              string
		minAgents, stale   int
		wantOrder          string
		wantStale, wantErr bool
	}{
		{"empty becomes off", "", 0, 0, SubProbeOrderOff, true, false},
		{"hide", SubProbeOrderHide, 2, 60, SubProbeOrderHide, false, false},
		{"unknown order", "drop", 0, 30, "", false, true},
		{"too many agents", SubProbeOrderReorder, SubProbeMinAgentsMax + 1, 30, "", false, true},
		{"stale too short", SubProbeOrderReorder, 0, SubProbeStaleMinutesMin - 1, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			all := &entity.AllSetting{SubProbeOrder: tc.order, SubProbeMinAgents: tc.minAgents, SubProbeStaleMinutes: tc.stale}
			err := normalizeSubProbeSettings(all)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if !tc.wantErr && all.SubProbeOrder != tc.wantOrder {
				t.Fatalf("order = %q, want %q", all.SubProbeOrder, tc.wantOrder)
			}
			if tc.wantStale && all.SubProbeStaleMinutes != SubProbeStaleMinutesDefault {
				t.Fatalf("stale = %d, want the default", all.SubProbeStaleMinutes)
			}
		})
	}
}

func TestSubProbePolicyDefaultsOff(t *testing.T) {
	setupBulkDB(t)
	s := &SettingService{}
	if p := s.SubProbePolicy(); p.Mode != SubProbeOrderOff {
		t.Fatalf("default policy = %+v, want off", p)
	}
	if err := s.setString("subProbeOrder", SubProbeOrderHide); err != nil {
		t.Fatal(err)
	}
	if err := s.setString("subProbeStaleMinutes", "99999"); err != nil {
		t.Fatal(err)
	}
	if p := s.SubProbePolicy(); p.Mode != SubProbeOrderHide || p.StaleMinutes != SubProbeStaleMinutesMax || p.MinAgents != 0 {
		t.Fatalf("policy = %+v, want hide with the stale window clamped", p)
	}
}

// The Probes page shows per endpoint what subscriptions do with it and why an agent is unused.
func TestReachProbeListShowsSubscriptionState(t *testing.T) {
	w := newSubProbeWorld(t)
	s := &SettingService{}
	if err := s.setString("subProbeOrder", SubProbeOrderHide); err != nil {
		t.Fatal(err)
	}
	a := w.agent(time.Minute, 1, 2, 4)
	b := w.agent(time.Minute, 2, 3)
	silent := w.agent(time.Hour, 3)
	w.check(a, "1@vpn.example.com:443", 2, time.Minute)
	w.check(a, "2@vpn.example.com:8443", 2, time.Minute)
	w.check(a, "4@vpn.example.com:7443", 0, time.Minute)
	w.check(b, "2@vpn.example.com:8443", 0, time.Minute)
	w.check(b, "3@vpn.example.com:9443", 2, time.Minute)
	w.check(silent, "3@vpn.example.com:9443", 0, time.Hour)
	views, err := w.svc.List()
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]string{}
	for _, v := range views {
		for _, tg := range v.Targets {
			state[strconv.Itoa(v.Id)+"/"+tg.Key] = tg.SubState
		}
	}
	id := func(p *model.ReachProbe, key string) string { return strconv.Itoa(p.Id) + "/" + key }
	if got := state[id(a, "1@vpn.example.com:443")]; got != ReachSubHidden {
		t.Fatalf("inbound 1 state = %q, want hidden", got)
	}
	if got := state[id(b, "2@vpn.example.com:8443")]; got != ReachSubUnconfirmed {
		t.Fatalf("inbound 2 (one of two agents) state = %q, want unconfirmed on every probe's row", got)
	}
	if got := state[id(b, "3@vpn.example.com:9443")]; got != ReachSubUnconfirmed {
		t.Fatalf("inbound 3 (silent second agent) state = %q, want unconfirmed", got)
	}
	if views[2].SubIgnored != ReachSubIgnoredSilent {
		t.Fatalf("silent probe reason = %q", views[2].SubIgnored)
	}
}
