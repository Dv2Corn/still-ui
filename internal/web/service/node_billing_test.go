package service

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// nodeClient seeds a client of a local inbound on this panel (the node side of a master).
func nodeClient(t *testing.T, db *gorm.DB, email string, port int, total int64) {
	t.Helper()
	settings := fmt.Sprintf(`{"clients": [{"email": %q, "enable": true}]}`, email)
	ib := &model.Inbound{UserId: 1, Tag: "in-" + email, Enable: true, Port: port, Protocol: model.VLESS, Settings: settings}
	if err := db.Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&ClientService{}).SyncInbound(db, ib.Id, []model.Client{{Email: email, Enable: true}}); err != nil {
		t.Fatal(err)
	}
	ct := xray.ClientTraffic{InboundId: ib.Id, Email: email, Enable: true}
	if err := db.Where("email = ?", email).Attrs(ct).FirstOrCreate(&ct).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(xray.ClientTraffic{}).Where("email = ?", email).Update("total", total).Error; err != nil {
		t.Fatal(err)
	}
}

func setNodeCounters(t *testing.T, db *gorm.DB, email string, up, down int64) {
	t.Helper()
	if err := db.Model(xray.ClientTraffic{}).Where("email = ?", email).
		Updates(map[string]any{"up": up, "down": down}).Error; err != nil {
		t.Fatal(err)
	}
}

func setBilling(t *testing.T, guid string, m float64) {
	t.Helper()
	if err := (&InboundService{}).SetMasterBillingMultiplier(guid, m); err != nil {
		t.Fatal(err)
	}
}

// runNodeQuotaLoop is the node's quota pass; it reports whether the client is still on.
func runNodeQuotaLoop(t *testing.T, db *gorm.DB, email string) bool {
	t.Helper()
	if _, _, _, err := (&InboundService{}).disableInvalidClients(db, newTrafficMutationBatch()); err != nil {
		t.Fatal(err)
	}
	return readTraffic(t, db, email).Enable
}

func TestNodeBillingCutsAtBilledQuota(t *testing.T) {
	cases := []struct {
		name               string
		m                  float64
		underUp, underDown int64
		atUp, atDown       int64
	}{
		{"half", 0.5, 600, 1398, 1000, 1000}, // billed 300+699 = 999, then 1000
		{"double", 2.0, 249, 250, 250, 250},  // billed 998, then 1000: raw half the quota
		{"one", 1, 499, 500, 500, 500},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := initTrafficTestDB(t)
			setBilling(t, "master-a", tc.m)
			email := "c-" + tc.name
			nodeClient(t, db, email, 42000+i, 1000)
			setNodeCounters(t, db, email, tc.underUp, tc.underDown)
			if !runNodeQuotaLoop(t, db, email) {
				t.Fatalf("node cut %s before the billed quota", email)
			}
			if got, _ := (&InboundService{}).DepletedClientEmails([]string{email}); len(got) != 0 {
				t.Fatalf("DepletedClientEmails = %v under the billed quota", got)
			}
			setNodeCounters(t, db, email, tc.atUp, tc.atDown)
			if runNodeQuotaLoop(t, db, email) {
				t.Fatalf("node kept %s on at the billed quota", email)
			}
		})
	}
}

func TestNodeBillingOneIsByteIdentical(t *testing.T) {
	db := initTrafficTestDB(t)
	setBilling(t, "master-a", 1)
	if got, _ := depletedCond(db); got != depletedClientsCondLocal {
		t.Fatalf("multiplier 1 changed the local predicate: %s", got)
	}
	setBilling(t, "master-a", 0.5)
	setBilling(t, "master-a", 1)
	var n int64
	db.Model(&model.Setting{}).Where("key = ?", nodeBillingSettingKey).Count(&n)
	if n != 0 || localBillingMultiplier(db) != 1 {
		t.Fatalf("pushing 1 must forget the entry: rows=%d m=%v", n, localBillingMultiplier(db))
	}
	seedClientRow(t, "x", 1, 0, 0, 1000)
	if err := (&InboundService{}).AcceptGlobalTraffic("master-a", []*xray.ClientTraffic{{Email: "x", Up: 1}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := depletedCond(db); got != depletedClientsCond {
		t.Fatalf("multiplier 1 changed the global predicate: %s", got)
	}
	if billedLocalUsage(depletedClientsClause, 1) != depletedClientsClause {
		t.Fatal("billedLocalUsage(…, 1) must return the clause unchanged")
	}
}

func TestNodeBillingPredicateIsAFloatLiteral(t *testing.T) {
	got := billedLocalUsage("total > 0 AND up + down >= total", 2)
	if want := "total > 0 AND ROUND(up * 2.0) + ROUND(down * 2.0) >= total"; got != want {
		t.Fatalf("billedLocalUsage = %q, want %q", got, want)
	}
}

func TestNodeBillingUsesTheSmallestMaster(t *testing.T) {
	db := initTrafficTestDB(t)
	setBilling(t, "master-a", 2)
	setBilling(t, "master-b", 0.5)
	if m := localBillingMultiplier(db); m != 0.5 {
		t.Fatalf("localBillingMultiplier = %v, want the smallest 0.5", m)
	}
	nodeClient(t, db, "two", 42010, 1000)
	setNodeCounters(t, db, "two", 900, 900)
	if !runNodeQuotaLoop(t, db, "two") {
		t.Fatal("a master billing 0.5 has not spent the quota yet")
	}
}

func TestNodeBillingIgnoresBadInput(t *testing.T) {
	db := initTrafficTestDB(t)
	for _, m := range []float64{0, 0.05, 101, -1} {
		setBilling(t, "master-a", m)
	}
	setBilling(t, "", 0.5)
	if m := localBillingMultiplier(db); m != 1 {
		t.Fatalf("out-of-range pushes stored %v", m)
	}
}

func TestNodeBillingSurvivesRestartAndOfflineMaster(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	path := filepath.Join(dbDir, "x-ui.db")
	initTrafficTestDBAt(t, path)
	db := database.GetDB()
	setBilling(t, "master-a", 0.5)
	nodeClient(t, db, "restart", 42020, 1000)
	if err := (&InboundService{}).AcceptGlobalTraffic("master-a", []*xray.ClientTraffic{{Email: "restart", Up: 100, Down: 100}}); err != nil {
		t.Fatal(err)
	}
	if err := database.CloseDB(); err != nil {
		t.Fatal(err)
	}
	if err := database.InitDB(path); err != nil {
		t.Fatal(err)
	}
	db = database.GetDB()
	// The master went quiet: its pushed rows are past the fresh window, only own counters bind.
	clock := globalTrafficClock(t, db, false)
	clock.Store(time.Now().Add(25 * time.Hour).UnixMilli())
	if got, _ := depletedCond(db); got != billedLocalUsage(depletedClientsCondLocal, 0.5) {
		t.Fatalf("offline master: predicate %s", got)
	}
	setNodeCounters(t, db, "restart", 700, 700)
	if !runNodeQuotaLoop(t, db, "restart") {
		t.Fatal("after restart with the master offline the node cut a client billed 700 of 1000")
	}
	setNodeCounters(t, db, "restart", 1000, 1000)
	if runNodeQuotaLoop(t, db, "restart") {
		t.Fatal("after restart the node kept a client on at the billed quota")
	}
}

func TestNodeBillingForgetsAMasterThatStopped(t *testing.T) {
	db := initTrafficTestDB(t)
	old := map[string]nodeBillingEntry{"gone": {M: 0.5, At: time.Now().Add(-nodeBillingTTL - time.Hour).UnixMilli()}}
	raw, _ := json.Marshal(old)
	if err := db.Create(&model.Setting{Key: nodeBillingSettingKey, Value: string(raw)}).Error; err != nil {
		t.Fatal(err)
	}
	if m := localBillingMultiplier(db); m != 1 {
		t.Fatalf("a master silent past the TTL still bills at %v", m)
	}
	setBilling(t, "gone", 0.5)
	if m := localBillingMultiplier(db); m != 0.5 {
		t.Fatalf("a fresh push must refresh the entry, got %v", m)
	}
}

func TestNodeBillingResetReenables(t *testing.T) {
	for i, m := range []float64{0.5, 2} {
		t.Run(fmt.Sprint(m), func(t *testing.T) {
			db := initTrafficTestDB(t)
			setBilling(t, "master-a", m)
			email := fmt.Sprintf("reset-%d", i)
			nodeClient(t, db, email, 42030+i, 1000)
			raw := int64(500 / m) // billed 500 each way: exactly the quota
			setNodeCounters(t, db, email, raw, raw)
			if runNodeQuotaLoop(t, db, email) {
				t.Fatal("not cut at the billed quota")
			}
			if err := db.Model(&model.ClientRecord{}).Where("email = ?", email).Update("enable", false).Error; err != nil {
				t.Fatal(err)
			}
			if hand, err := handDisabledEmails(db, []string{email}); err != nil || len(hand) != 0 {
				t.Fatalf("a quota cut read as a hand disable: %v %v", hand, err)
			}
			if err := (&InboundService{}).ResetClientTrafficByEmail(email); err != nil {
				t.Fatal(err)
			}
			if !runNodeQuotaLoop(t, db, email) {
				t.Fatal("a reset did not bring the client back on the node")
			}
		})
	}
}

func TestNodeBillingRenewalReenables(t *testing.T) {
	db := initTrafficTestDB(t)
	setBilling(t, "master-a", 0.5)
	nodeClient(t, db, "renew", 42040, 1000)
	setNodeCounters(t, db, "renew", 1000, 1000) // billed 1000 of 1000
	if runNodeQuotaLoop(t, db, "renew") {
		t.Fatal("not cut at the billed quota")
	}
	if !(&ClientService{}).renewalReenables("renew", 1500, 0) {
		t.Fatal("a renewal to 1500 must lift a client billed 1000 (raw 2000)")
	}
	if err := db.Model(xray.ClientTraffic{}).Where("email = ?", "renew").
		Updates(map[string]any{"total": 1500, "enable": true}).Error; err != nil {
		t.Fatal(err)
	}
	if !runNodeQuotaLoop(t, db, "renew") {
		t.Fatal("the renewed client was cut again on the node")
	}
}

func TestNodeBillingPeriodicResetSeesBilledUsage(t *testing.T) {
	db := initTrafficTestDB(t)
	setBilling(t, "master-a", 2)
	nodeClient(t, db, "cycle", 42050, 1000)
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", "cycle").
		Updates(map[string]any{"traffic_reset": "daily", "enable": false}).Error; err != nil {
		t.Fatal(err)
	}
	setNodeCounters(t, db, "cycle", 250, 250)
	cycles, err := (&ClientService{}).GetClientsByTrafficReset("daily")
	if err != nil || len(cycles) != 1 || !cycles[0].Depleted() {
		t.Fatalf("a client billed 1000 of 1000 must read as depleted: %+v %v", cycles, err)
	}
}

// The master half: with the multiplier, the node's disable at the billed quota latches.
func TestMasterLatchesNodeCutAtBilledQuota(t *testing.T) {
	for i, m := range []float64{0.5, 2} {
		t.Run(fmt.Sprint(m), func(t *testing.T) {
			db := initTrafficTestDB(t)
			seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
			setNodeColumns(t, db, 1, map[string]any{"traffic_multiplier": m})
			email := fmt.Sprintf("latch-%d@x", i)
			createNodeInboundWithClient(t, db, 1, "n1-in", 41001, email)
			if err := db.Model(xray.ClientTraffic{}).Where("email = ?", email).Update("total", 1000).Error; err != nil {
				t.Fatal(err)
			}
			if got := NodeBillingMultiplier(new(readNodeRow(t, db, 1))); got != m {
				t.Fatalf("pushed multiplier %v, want %v", got, m)
			}
			svc := &InboundService{}
			on := fmt.Sprintf(`{"clients":[{"email":%q,"enable":true}]}`, email)
			off := fmt.Sprintf(`{"clients":[{"email":%q,"enable":false}]}`, email)
			syncNodeWithSettings(t, svc, 1, "n1-in", on, xray.ClientTraffic{Email: email, Total: 1000, Enable: true})
			raw := int64(500 / m)
			syncNodeWithSettings(t, svc, 1, "n1-in", off, xray.ClientTraffic{Email: email, Up: raw, Down: raw, Total: 1000, Enable: false})
			ct := readTraffic(t, database.GetDB(), email)
			assertUpDown(t, ct, 500, 500, "billed")
			if ct.Enable {
				t.Fatal("the master treated the node's cut at the billed quota as stale")
			}
		})
	}
}

func TestNodeBillingUnsetNodeMultiplierPushesOne(t *testing.T) {
	if got := NodeBillingMultiplier(&model.Node{}); got != 1 {
		t.Fatalf("an unset multiplier pushes %v", got)
	}
}
