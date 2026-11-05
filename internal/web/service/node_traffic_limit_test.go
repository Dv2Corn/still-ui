package service

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func readNodeRow(t *testing.T, db *gorm.DB, id int) model.Node {
	t.Helper()
	var n model.Node
	if err := db.Where("id = ?", id).First(&n).Error; err != nil {
		t.Fatalf("read node %d: %v", id, err)
	}
	return n
}

func setNodeColumns(t *testing.T, db *gorm.DB, id int, cols map[string]any) {
	t.Helper()
	if err := db.Model(model.Node{}).Where("id = ?", id).Updates(cols).Error; err != nil {
		t.Fatalf("update node %d: %v", id, err)
	}
}

func TestNodeTrafficPeriodStart(t *testing.T) {
	vlat := time.FixedZone("UTC+10", 10*3600)
	at := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, vlat) }
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, vlat) }
	cases := []struct {
		name      string
		now       time.Time
		resetDay  int
		wantStart time.Time
		wantNext  time.Time
	}{
		{"31st in February resets on the 28th", at(2027, time.March, 10, 12, 0), 31, day(2027, time.February, 28), day(2027, time.March, 31)},
		{"31st in a leap February resets on the 29th", at(2028, time.March, 15, 0, 0), 31, day(2028, time.February, 29), day(2028, time.March, 31)},
		{"31st in April resets on the 30th", at(2027, time.April, 30, 0, 0), 31, day(2027, time.April, 30), day(2027, time.May, 31)},
		{"the day before a clamped reset is still the old period", at(2027, time.April, 29, 23, 59), 31, day(2027, time.March, 31), day(2027, time.April, 30)},
		{"reset day crosses the year", at(2027, time.January, 4, 9, 0), 15, day(2026, time.December, 15), day(2027, time.January, 15)},
		{"midnight belongs to the new period", at(2027, time.June, 1, 0, 0), 1, day(2027, time.June, 1), day(2027, time.July, 1)},
		{"the panel time zone decides the date, not UTC", time.Date(2027, time.May, 31, 15, 0, 0, 0, time.UTC), 1, day(2027, time.June, 1), day(2027, time.July, 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nodeTrafficPeriodStart(c.now, c.resetDay, vlat); !got.Equal(c.wantStart) {
				t.Errorf("period start = %v, want %v", got, c.wantStart)
			}
			if got := nextCalendarRenewal(c.now, c.resetDay, vlat); !got.Equal(c.wantNext) {
				t.Errorf("next reset = %v, want %v", got, c.wantNext)
			}
		})
	}
}

// The multiplier bills node traffic once: a retried or repeated snapshot, a panel
// restart in between, and the node's own counter all stay at the raw delta.
func TestNodeTrafficMultiplierAppliedOnceAcrossRetries(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	dbPath := filepath.Join(dbDir, "x-ui.db")
	initTrafficTestDBAt(t, dbPath)
	db := database.GetDB()
	seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
	setNodeColumns(t, db, 1, map[string]any{"traffic_multiplier": 2.5})
	createNodeInboundWithClient(t, db, 1, "n1-in", 41001, "bill@x")
	settings := `{"clients":[{"email":"bill@x","enable":true}]}`
	svc := &InboundService{}

	syncNodeWithSettings(t, svc, 1, "n1-in", settings, xray.ClientTraffic{Email: "bill@x", Up: 1000, Down: 2000, Enable: true})
	for range 3 {
		syncNodeWithSettings(t, svc, 1, "n1-in", settings, xray.ClientTraffic{Email: "bill@x", Up: 1100, Down: 2400, Enable: true})
	}
	assertUpDown(t, readTraffic(t, database.GetDB(), "bill@x"), 250, 1000, "after one delta merged and retried")
	if got := readNodeRow(t, database.GetDB(), 1).TrafficUsed; got != 500 {
		t.Fatalf("node traffic used = %d, want the raw 500", got)
	}

	if err := database.CloseDB(); err != nil {
		t.Fatal(err)
	}
	if err := database.InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	svc = &InboundService{}
	syncNodeWithSettings(t, svc, 1, "n1-in", settings, xray.ClientTraffic{Email: "bill@x", Up: 1100, Down: 2400, Enable: true})
	syncNodeWithSettings(t, svc, 1, "n1-in", settings, xray.ClientTraffic{Email: "bill@x", Up: 1101, Down: 2401, Enable: true})
	db = database.GetDB()
	assertUpDown(t, readTraffic(t, db, "bill@x"), 253, 1003, "after a restart and one more byte each way")
	if got := readNodeRow(t, db, 1).TrafficUsed; got != 502 {
		t.Fatalf("node traffic used after restart = %d, want 502", got)
	}
	var base model.NodeClientTraffic
	if err := db.Where("node_id = ? AND email = ?", 1, "bill@x").First(&base).Error; err != nil {
		t.Fatal(err)
	}
	if base.Up != 1101 || base.Down != 2401 {
		t.Fatalf("baseline = %d/%d, want the node's raw 1101/2401", base.Up, base.Down)
	}
}

func initTrafficTestDBAt(t *testing.T, path string) {
	t.Helper()
	if err := database.InitDB(path); err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
}

// A client attached to two inbounds of one node is reported twice with the same
// counter; it must enter the node's usage and the client's bill once.
func TestNodeTrafficMultiAttachedClientCountedOnce(t *testing.T) {
	db := initTrafficTestDB(t)
	seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
	setNodeColumns(t, db, 1, map[string]any{"traffic_multiplier": 2})
	createNodeInboundWithClient(t, db, 1, "n1-a", 41001, "multi")
	createNodeInboundWithClient(t, db, 1, "n1-b", 41002, "multi")
	settings := `{"clients": [{"email": "multi", "enable": true}]}`
	svc := &InboundService{}
	sync := func(v int64) {
		t.Helper()
		snap := &runtime.TrafficSnapshot{Inbounds: []*model.Inbound{
			{Tag: "n1-a", Settings: settings, ClientStats: []xray.ClientTraffic{{Email: "multi", Up: v, Down: v, Enable: true}}},
			{Tag: "n1-b", Settings: settings, ClientStats: []xray.ClientTraffic{{Email: "multi", Up: v, Down: v, Enable: true}}},
		}}
		if _, err := svc.setRemoteTrafficLocked(1, snap, false, false); err != nil {
			t.Fatalf("sync: %v", err)
		}
	}
	sync(100)
	sync(150)
	assertUpDown(t, readTraffic(t, db, "multi"), 100, 100, "50 each way billed at x2, once")
	if got := readNodeRow(t, db, 1).TrafficUsed; got != 100 {
		t.Fatalf("node traffic used = %d, want 100", got)
	}
}

// Traffic of an inbound the node forwards from its own sub-node is billed to the
// client, but it never crossed this node's server, so the node's usage skips it.
func TestNodeTrafficForwardedInboundNotNodeUsage(t *testing.T) {
	db := initTrafficTestDB(t)
	seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
	createNodeInboundWithClient(t, db, 1, "own-in", 41001, "own")
	createNodeInboundWithClient(t, db, 1, "sub-in", 41002, "far")
	svc := &InboundService{}
	sync := func(v int64) {
		t.Helper()
		snap := &runtime.TrafficSnapshot{Inbounds: []*model.Inbound{
			{Tag: "own-in", Settings: `{"clients":[{"email":"own","enable":true}]}`, ClientStats: []xray.ClientTraffic{{Email: "own", Up: v, Down: v, Enable: true}}},
			{Tag: "sub-in", OriginNodeGuid: "sub-node-guid", Settings: `{"clients":[{"email":"far","enable":true}]}`, ClientStats: []xray.ClientTraffic{{Email: "far", Up: 10 * v, Down: 10 * v, Enable: true}}},
		}}
		if _, err := svc.setRemoteTrafficLocked(1, snap, false, false); err != nil {
			t.Fatalf("sync: %v", err)
		}
	}
	sync(10)
	sync(20)
	assertUpDown(t, readTraffic(t, db, "far"), 100, 100, "forwarded client still billed")
	if got := readNodeRow(t, db, 1).TrafficUsed; got != 20 {
		t.Fatalf("node traffic used = %d, want only the node's own 20", got)
	}
}

func collectNodeTrafficEvents(t *testing.T) func() []eventbus.Event {
	t.Helper()
	bus := eventbus.New(64)
	events := make(chan eventbus.Event, 64)
	bus.Subscribe("test", func(e eventbus.Event) { events <- e })
	SetEventBus(bus)
	t.Cleanup(func() {
		SetEventBus(nil)
		bus.Stop()
	})
	return func() []eventbus.Event {
		var out []eventbus.Event
		deadline := time.After(200 * time.Millisecond)
		for {
			select {
			case e := <-events:
				if e.Type == eventbus.EventNodeTraffic {
					out = append(out, e)
				}
			case <-deadline:
				return out
			}
		}
	}
}

func alertSummary(events []eventbus.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		data, _ := e.Data.(*eventbus.HealthAlertData)
		if data == nil {
			out = append(out, "?")
			continue
		}
		out = append(out, data.Phase+":"+data.Params["Level"]+":"+data.Params["Percent"])
	}
	return out
}

func TestNodeTrafficLimitAlertsOncePerPeriod(t *testing.T) {
	db := initTrafficTestDB(t)
	drain := collectNodeTrafficEvents(t)
	seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
	if err := (&NodeService{}).SetTrafficLimit(1, &NodeTrafficLimitRequest{
		TrafficLimitBytes: 1000, TrafficResetDay: 31, TrafficWarnPercent: 80,
		TrafficLimitAction: NodeTrafficActionNotify, TrafficMultiplier: 1,
	}); err != nil {
		t.Fatal(err)
	}
	loc := (&NodeService{}).trafficLocation()
	feb := time.Date(2027, time.February, 10, 12, 0, 0, 0, loc)
	evaluate := func(now time.Time, used int64) []string {
		t.Helper()
		if used >= 0 {
			setNodeColumns(t, db, 1, map[string]any{"traffic_used": used})
		}
		(&NodeService{}).EvaluateTrafficLimits(now)
		return alertSummary(drain())
	}

	steps := []struct {
		name string
		now  time.Time
		used int64
		want []string
	}{
		{"under the threshold", feb, 799, nil},
		{"crossing the warning", feb, 800, []string{"raised:warn:80"}},
		{"still warned, more usage", feb.Add(time.Hour), 950, nil},
		{"crossing the limit", feb.Add(2 * time.Hour), 1000, []string{"raised:limit:100"}},
		{"over the limit stays quiet", feb.Add(3 * time.Hour), 1500, nil},
		{"the 28th of February is the clamped reset of the 31st", time.Date(2027, time.February, 28, 0, 0, 1, 0, loc), -1, []string{"resolved:under:0"}},
		{"usage after the reset starts from zero", time.Date(2027, time.March, 2, 0, 0, 0, 0, loc), -1, nil},
	}
	for _, s := range steps {
		if got := evaluate(s.now, s.used); !slices.Equal(got, s.want) {
			t.Fatalf("%s: alerts %v, want %v", s.name, got, s.want)
		}
	}
	n := readNodeRow(t, db, 1)
	if n.TrafficUsed != 0 || n.TrafficAlertLevel != nodeTrafficLevelUnder {
		t.Fatalf("after the reset used=%d level=%d, want 0/0", n.TrafficUsed, n.TrafficAlertLevel)
	}
	if want := time.Date(2027, time.February, 28, 0, 0, 0, 0, loc).UnixMilli(); n.TrafficPeriodStart != want {
		t.Fatalf("period start = %d, want %d", n.TrafficPeriodStart, want)
	}
}

// The announced level lives in the database: a panel restart over the limit must not
// send the alert again.
func TestNodeTrafficLimitAlertSurvivesRestart(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	dbPath := filepath.Join(dbDir, "x-ui.db")
	initTrafficTestDBAt(t, dbPath)
	drain := collectNodeTrafficEvents(t)
	db := database.GetDB()
	seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
	setNodeColumns(t, db, 1, map[string]any{"traffic_limit_bytes": 100, "traffic_used": 150})
	now := time.Now()
	(&NodeService{}).EvaluateTrafficLimits(now)
	if got := alertSummary(drain()); !slices.Equal(got, []string{"raised:limit:150"}) {
		t.Fatalf("first evaluation alerts %v", got)
	}
	if err := database.CloseDB(); err != nil {
		t.Fatal(err)
	}
	if err := database.InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	(&NodeService{}).EvaluateTrafficLimits(now.Add(time.Minute))
	if got := alertSummary(drain()); len(got) != 0 {
		t.Fatalf("alerts after restart %v, want none", got)
	}
}

func nodeInboundEnabled(t *testing.T, db *gorm.DB, tag string) bool {
	t.Helper()
	var ib model.Inbound
	if err := db.Where("tag = ?", tag).First(&ib).Error; err != nil {
		t.Fatalf("read inbound %q: %v", tag, err)
	}
	return ib.Enable
}

func TestNodeTrafficLimitDisablesInboundsUntilReset(t *testing.T) {
	db := initTrafficTestDB(t)
	collectNodeTrafficEvents(t)
	seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
	seedNodeRow(t, db, &model.Node{Id: 2, Name: "n2", Address: "127.0.0.2", Port: 2096, ApiToken: "tok", Enable: true})
	createNodeInbound(t, db, 1, "n1-a", 41001)
	createNodeInbound(t, db, 1, "n1-b", 41002)
	createNodeInbound(t, db, 1, "n1-off", 41003)
	createNodeInbound(t, db, 2, "n2-a", 41004)
	if err := db.Model(model.Inbound{}).Where("tag = ?", "n1-off").Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&NodeService{}).SetTrafficLimit(1, &NodeTrafficLimitRequest{
		TrafficLimitBytes: 1000, TrafficResetDay: 1, TrafficWarnPercent: 0,
		TrafficLimitAction: NodeTrafficActionDisable, TrafficMultiplier: 1,
	}); err != nil {
		t.Fatal(err)
	}
	loc := (&NodeService{}).trafficLocation()
	may := time.Date(2027, time.May, 20, 12, 0, 0, 0, loc)
	svc := &NodeService{}
	svc.EvaluateTrafficLimits(may)
	setNodeColumns(t, db, 1, map[string]any{"traffic_used": 1000, "config_dirty": false})

	svc.EvaluateTrafficLimits(may.Add(time.Minute))
	for tag, want := range map[string]bool{"n1-a": false, "n1-b": false, "n1-off": false, "n2-a": true} {
		if got := nodeInboundEnabled(t, db, tag); got != want {
			t.Fatalf("over the limit: %s enable=%v, want %v", tag, got, want)
		}
	}
	n := readNodeRow(t, db, 1)
	if !n.TrafficLimited || !n.ConfigDirty {
		t.Fatalf("limited=%v dirty=%v, want both true so reconcile pushes the change", n.TrafficLimited, n.ConfigDirty)
	}

	// An admin turning one back on by hand while limited is left alone.
	if _, err := (&InboundService{}).SetInboundEnable(readInboundID(t, db, "n1-b"), true); err != nil {
		t.Fatal(err)
	}
	svc.EvaluateTrafficLimits(may.Add(2 * time.Minute))
	if !nodeInboundEnabled(t, db, "n1-b") {
		t.Fatal("the limit turned off an inbound the admin re-enabled")
	}

	svc.EvaluateTrafficLimits(time.Date(2027, time.June, 1, 0, 0, 5, 0, loc))
	for tag, want := range map[string]bool{"n1-a": true, "n1-b": true, "n1-off": false, "n2-a": true} {
		if got := nodeInboundEnabled(t, db, tag); got != want {
			t.Fatalf("after the reset: %s enable=%v, want %v", tag, got, want)
		}
	}
	if n := readNodeRow(t, db, 1); n.TrafficLimited || len(n.TrafficDisabledInbounds) != 0 || n.TrafficUsed != 0 {
		t.Fatalf("after the reset limited=%v disabled=%v used=%d", n.TrafficLimited, n.TrafficDisabledInbounds, n.TrafficUsed)
	}
}

func readInboundID(t *testing.T, db *gorm.DB, tag string) int {
	t.Helper()
	var ib model.Inbound
	if err := db.Where("tag = ?", tag).First(&ib).Error; err != nil {
		t.Fatal(err)
	}
	return ib.Id
}

// Raising the limit above the usage, or switching to notify, ends the limit at once.
func TestNodeTrafficLimitLiftedBySettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  NodeTrafficLimitRequest
	}{
		{"limit raised", NodeTrafficLimitRequest{TrafficLimitBytes: 5000, TrafficResetDay: 1, TrafficWarnPercent: 0, TrafficLimitAction: NodeTrafficActionDisable, TrafficMultiplier: 1}},
		{"action notify", NodeTrafficLimitRequest{TrafficLimitBytes: 1000, TrafficResetDay: 1, TrafficWarnPercent: 0, TrafficLimitAction: NodeTrafficActionNotify, TrafficMultiplier: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := initTrafficTestDB(t)
			drain := collectNodeTrafficEvents(t)
			seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
			createNodeInbound(t, db, 1, "n1-a", 41001)
			svc := &NodeService{}
			if err := svc.SetTrafficLimit(1, &NodeTrafficLimitRequest{TrafficLimitBytes: 1000, TrafficResetDay: 1, TrafficLimitAction: NodeTrafficActionDisable, TrafficMultiplier: 1}); err != nil {
				t.Fatal(err)
			}
			setNodeColumns(t, db, 1, map[string]any{"traffic_used": 2000})
			now := time.Now()
			svc.EvaluateTrafficLimits(now)
			if nodeInboundEnabled(t, db, "n1-a") {
				t.Fatal("inbound still on over the limit")
			}
			if err := svc.SetTrafficLimit(1, &tc.req); err != nil {
				t.Fatal(err)
			}
			svc.EvaluateTrafficLimits(now.Add(time.Minute))
			if !nodeInboundEnabled(t, db, "n1-a") {
				t.Fatal("inbound not turned back on")
			}
			want := []string{"raised:limit:200"}
			if tc.req.TrafficLimitBytes > 2000 {
				want = append(want, "resolved:under:40")
			}
			if got := alertSummary(drain()); !slices.Equal(got, want) {
				t.Fatalf("alerts %v, want %v", got, want)
			}
		})
	}
}

func TestNodeTrafficLimitRequestValidation(t *testing.T) {
	valid := NodeTrafficLimitRequest{TrafficLimitBytes: 0, TrafficResetDay: 1, TrafficWarnPercent: 80, TrafficLimitAction: NodeTrafficActionNotify, TrafficMultiplier: 1}
	for _, tc := range []struct {
		name   string
		mutate func(*NodeTrafficLimitRequest)
		ok     bool
	}{
		{"defaults", func(*NodeTrafficLimitRequest) {}, true},
		{"negative limit", func(r *NodeTrafficLimitRequest) { r.TrafficLimitBytes = -1 }, false},
		{"reset day 0", func(r *NodeTrafficLimitRequest) { r.TrafficResetDay = 0 }, false},
		{"reset day 32", func(r *NodeTrafficLimitRequest) { r.TrafficResetDay = 32 }, false},
		{"warn 101", func(r *NodeTrafficLimitRequest) { r.TrafficWarnPercent = 101 }, false},
		{"unknown action", func(r *NodeTrafficLimitRequest) { r.TrafficLimitAction = "block" }, false},
		{"multiplier zero", func(r *NodeTrafficLimitRequest) { r.TrafficMultiplier = 0 }, false},
		{"multiplier 100", func(r *NodeTrafficLimitRequest) { r.TrafficMultiplier = 100 }, true},
		{"multiplier 100.5", func(r *NodeTrafficLimitRequest) { r.TrafficMultiplier = 100.5 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			tc.mutate(&r)
			if err := r.validate(); (err == nil) != tc.ok {
				t.Fatalf("validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// Saving the quota must not mark the node dirty: nothing about it is pushed.
func TestSetTrafficLimitKeepsNodeClean(t *testing.T) {
	db := initTrafficTestDB(t)
	seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
	before := readNodeRow(t, db, 1)
	if before.TrafficMultiplier != 1 || before.TrafficResetDay != 1 || before.TrafficWarnPercent != 80 || before.TrafficLimitAction != NodeTrafficActionNotify {
		t.Fatalf("new node defaults = %v/%d/%d/%q", before.TrafficMultiplier, before.TrafficResetDay, before.TrafficWarnPercent, before.TrafficLimitAction)
	}
	if err := (&NodeService{}).SetTrafficLimit(1, &NodeTrafficLimitRequest{TrafficLimitBytes: 10, TrafficResetDay: 5, TrafficWarnPercent: 0, TrafficLimitAction: NodeTrafficActionDisable, TrafficMultiplier: 1.5}); err != nil {
		t.Fatal(err)
	}
	n := readNodeRow(t, db, 1)
	if n.ConfigDirty {
		t.Fatal("saving the traffic limit marked the node dirty")
	}
	if got := fmt.Sprintf("%d/%d/%d/%s/%v", n.TrafficLimitBytes, n.TrafficResetDay, n.TrafficWarnPercent, n.TrafficLimitAction, n.TrafficMultiplier); got != "10/5/0/disable/1.5" {
		t.Fatalf("stored %s", got)
	}
	if err := (&NodeService{}).SetTrafficLimit(99, &NodeTrafficLimitRequest{TrafficResetDay: 1, TrafficLimitAction: NodeTrafficActionNotify, TrafficMultiplier: 1}); err == nil {
		t.Fatal("saving the limit of a missing node succeeded")
	}
}

// On a 0.5 node a client at 1200 raw bytes of a 1000-byte quota is billed 600: a disable the node
// reports on raw counters (an older node) is stale and must not cut it at half its quota.
func TestNodeTrafficMultiplierStaleDisableUsesBilledUsage(t *testing.T) {
	db := initTrafficTestDB(t)
	seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
	setNodeColumns(t, db, 1, map[string]any{"traffic_multiplier": 0.5})
	createNodeInboundWithClient(t, db, 1, "n1-in", 41001, "half@x")
	if err := db.Model(xray.ClientTraffic{}).Where("email = ?", "half@x").Update("total", 1000).Error; err != nil {
		t.Fatal(err)
	}
	svc := &InboundService{}
	syncNodeWithSettings(t, svc, 1, "n1-in", `{"clients":[{"email":"half@x","enable":true}]}`,
		xray.ClientTraffic{Email: "half@x", Up: 0, Down: 0, Total: 1000, Enable: true})
	syncNodeWithSettings(t, svc, 1, "n1-in", `{"clients":[{"email":"half@x","enable":false}]}`,
		xray.ClientTraffic{Email: "half@x", Up: 600, Down: 600, Total: 1000, Enable: false})
	ct := readTraffic(t, database.GetDB(), "half@x")
	assertUpDown(t, ct, 300, 300, "billed at 0.5")
	if !ct.Enable {
		t.Fatal("the node's raw-counter disable cut a client billed 600 of 1000")
	}
}
