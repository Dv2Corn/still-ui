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

func pushFrom(t *testing.T, guid, email string, up, down int64) {
	t.Helper()
	if err := (&InboundService{}).AcceptGlobalTraffic(guid, []*xray.ClientTraffic{{Email: email, Up: up, Down: down}}); err != nil {
		t.Fatal(err)
	}
}

// goOffline ages every pushed row past the fresh window: the master stopped pushing.
func goOffline(t *testing.T, db *gorm.DB) {
	t.Helper()
	clock := globalTrafficClock(t, db, false)
	clock.Store(time.Now().Add(25 * time.Hour).UnixMilli())
}

// Without a fresh push the node bills its own counters at that client's master's multiplier.
func TestNodeBillingFallbackCutsAtBilledQuota(t *testing.T) {
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
			pushFrom(t, "master-a", email, 0, 0)
			goOffline(t, db)
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

// With a fresh push a billed client is judged by its master's count, which bills each delta at
// the rate of its time: raising ×1 to ×2 mid-period does not re-bill the traffic before it.
func TestNodeBillingFreshPushIsTheMastersCount(t *testing.T) {
	for i, tc := range []struct {
		m       float64
		raw     int64 // node counter, per direction
		under   int64 // master's billed count just below the quota, per direction
		quota   int64
		earlyAt string
	}{
		{2, 30, 49, 100, "60 raw would bill 120 at today's ×2"},
		{0.5, 1000, 499, 1000, "2000 raw is over the quota unbilled"},
	} {
		t.Run(fmt.Sprint(tc.m), func(t *testing.T) {
			db := initTrafficTestDB(t)
			setBilling(t, "master-a", tc.m)
			email := fmt.Sprintf("fresh-%d", i)
			nodeClient(t, db, email, 42060+i, tc.quota)
			setNodeCounters(t, db, email, tc.raw, tc.raw)
			pushFrom(t, "master-a", email, tc.under, tc.under)
			if !runNodeQuotaLoop(t, db, email) {
				t.Fatalf("cut before the master's count reached the quota (%s)", tc.earlyAt)
			}
			pushFrom(t, "master-a", email, tc.quota/2, tc.quota/2)
			if runNodeQuotaLoop(t, db, email) {
				t.Fatal("kept on once the master's count reached the quota")
			}
		})
	}
}

// Each client follows its own master: ×1 and ×2 masters on one node, and a client of nobody.
func TestNodeBillingPerClientMaster(t *testing.T) {
	db := initTrafficTestDB(t)
	setBilling(t, "master-a", 1)
	setBilling(t, "master-b", 2)
	for i, e := range []string{"of-a", "of-b", "local"} {
		nodeClient(t, db, e, 42070+i, 1000)
	}
	pushFrom(t, "master-a", "of-a", 0, 0)
	pushFrom(t, "master-b", "of-b", 0, 0)
	goOffline(t, db)
	for _, e := range []string{"of-a", "of-b", "local"} {
		setNodeCounters(t, db, e, 300, 300)
	}
	if _, _, _, err := (&InboundService{}).disableInvalidClients(db, newTrafficMutationBatch()); err != nil {
		t.Fatal(err)
	}
	for e, on := range map[string]bool{"of-a": true, "of-b": false, "local": true} {
		if got := readTraffic(t, db, e).Enable; got != on {
			t.Errorf("%s at 600 raw of 1000: enabled=%v, want %v", e, got, on)
		}
	}
	for _, e := range []string{"of-a", "local"} {
		setNodeCounters(t, db, e, 500, 500)
	}
	if _, _, _, err := (&InboundService{}).disableInvalidClients(db, newTrafficMutationBatch()); err != nil {
		t.Fatal(err)
	}
	if readTraffic(t, db, "of-a").Enable || readTraffic(t, db, "local").Enable {
		t.Fatal("×1 and master-less clients must be cut at their full raw quota")
	}
}

func TestNodeBillingOneIsByteIdentical(t *testing.T) {
	db := initTrafficTestDB(t)
	setBilling(t, "master-a", 1)
	if got := liveMasterMultipliers(db); got["master-a"] != 1 {
		t.Fatalf("a ×1 master must be kept, got %v", got)
	}
	if got, _ := depletedCond(db); got != depletedClientsCondLocal {
		t.Fatalf("multiplier 1 changed the local predicate: %s", got)
	}
	seedClientRow(t, "x", 1, 0, 0, 1000)
	pushFrom(t, "master-a", "x", 1, 0)
	if got, _ := depletedCond(db); got != depletedClientsCond {
		t.Fatalf("multiplier 1 changed the global predicate: %s", got)
	}
	if got, args := withBilledQuota(db, depletedClientsClause, []any{int64(7)}); got != depletedClientsClause || len(args) != 1 {
		t.Fatal("withBilledQuota with ×1 masters must return the clause unchanged")
	}
	rows := []xray.ClientTraffic{{Email: "x", Up: 5, Down: 6}}
	billedUsage(db, rows)
	if rows[0].Up != 5 || rows[0].Down != 6 {
		t.Fatalf("billedUsage changed a ×1 row: %+v", rows[0])
	}
}

func TestNodeBillingIgnoresBadInput(t *testing.T) {
	db := initTrafficTestDB(t)
	for _, m := range []float64{0, 0.05, 101, -1} {
		setBilling(t, "master-a", m)
	}
	setBilling(t, "", 0.5)
	if got := liveMasterMultipliers(db); len(got) != 0 {
		t.Fatalf("out-of-range pushes stored %v", got)
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
	pushFrom(t, "master-a", "restart", 100, 100)
	if err := database.CloseDB(); err != nil {
		t.Fatal(err)
	}
	if err := database.InitDB(path); err != nil {
		t.Fatal(err)
	}
	db = database.GetDB()
	goOffline(t, db)
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
	if got := liveMasterMultipliers(db); len(got) != 0 {
		t.Fatalf("a master silent past the TTL still bills: %v", got)
	}
	setBilling(t, "gone", 0.5)
	if got := liveMasterMultipliers(db); got["gone"] != 0.5 {
		t.Fatalf("a fresh push must refresh the entry, got %v", got)
	}
}

func TestNodeBillingResetReenables(t *testing.T) {
	for i, m := range []float64{0.5, 2} {
		t.Run(fmt.Sprint(m), func(t *testing.T) {
			db := initTrafficTestDB(t)
			setBilling(t, "master-a", m)
			email := fmt.Sprintf("reset-%d", i)
			nodeClient(t, db, email, 42030+i, 1000)
			pushFrom(t, "master-a", email, 0, 0)
			goOffline(t, db)
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
	pushFrom(t, "master-a", "renew", 0, 0)
	goOffline(t, db)
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
	pushFrom(t, "master-a", "cycle", 0, 0)
	goOffline(t, db)
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

// Raising ×1 to ×2 mid-period: the master's own count (old traffic at ×1) decides, so a node's
// disable judged on its whole counter at ×2 does not latch.
func TestMasterIgnoresANodeCutThatRebillsHistory(t *testing.T) {
	db := initTrafficTestDB(t)
	seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
	const email = "midway@x"
	createNodeInboundWithClient(t, db, 1, "n1-in", 41001, email)
	if err := db.Model(xray.ClientTraffic{}).Where("email = ?", email).Update("total", 100).Error; err != nil {
		t.Fatal(err)
	}
	svc := &InboundService{}
	on := fmt.Sprintf(`{"clients":[{"email":%q,"enable":true}]}`, email)
	off := fmt.Sprintf(`{"clients":[{"email":%q,"enable":false}]}`, email)
	syncNodeWithSettings(t, svc, 1, "n1-in", on, xray.ClientTraffic{Email: email, Total: 100, Enable: true})
	syncNodeWithSettings(t, svc, 1, "n1-in", on, xray.ClientTraffic{Email: email, Up: 30, Down: 30, Total: 100, Enable: true})
	setNodeColumns(t, db, 1, map[string]any{"traffic_multiplier": 2.0})
	syncNodeWithSettings(t, svc, 1, "n1-in", off, xray.ClientTraffic{Email: email, Up: 35, Down: 35, Total: 100, Enable: false})
	ct := readTraffic(t, database.GetDB(), email)
	assertUpDown(t, ct, 40, 40, "60 at ×1 plus 10 at ×2")
	if !ct.Enable {
		t.Fatal("the master latched a cut its own count (80 of 100) contradicts")
	}
}
