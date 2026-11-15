package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Go binds any key that folds to "clients", the claim check reads the exact one: a node
// listing a master-only client under a folded key must not bill, change or adopt it.
func TestNodeCannotListMasterOnlyClientUnderAFoldedKey(t *testing.T) {
	const victim = `{"email":"victim@x","id":"99999999-9999-4999-8999-999999999999","subId":"victim-sub","enable":false,"expiryTime":1,"totalGB":1}`
	for _, hostile := range []string{
		`{"Clients":[` + victim + `]}`,
		`{"clients":[],"Clients":[` + victim + `]}`,
		`{"CLIENTS":[` + victim + `],"clients":[]}`,
		`{"clientſ":[` + victim + `]}`,
		`{"clıents":[` + victim + `]}`,
		`{"clİents":[` + victim + `]}`,
		`{"\u0043lients":[` + victim + `]}`,
	} {
		t.Run(hostile[:12], func(t *testing.T) {
			db := initTrafficTestDB(t)
			svc := &InboundService{}
			seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
			master := seedMasterInbound(t, db, "master-in", 40001,
				`{"email":"victim@x","id":"11111111-1111-4111-8111-111111111111","subId":"victim-sub","enable":true}`)
			if err := db.Create(&xray.ClientTraffic{InboundId: master.Id, Email: "victim@x", Enable: true}).Error; err != nil {
				t.Fatalf("seed traffic: %v", err)
			}
			createNodeInbound(t, db, 1, "n1-in", 41001)
			for _, up := range []int64{0, 500} {
				syncNodeWithSettings(t, svc, 1, "n1-in", hostile,
					xray.ClientTraffic{Email: "victim@x", Up: up, Down: up, Enable: false, ExpiryTime: 1, Total: 1})
			}
			if got := readTraffic(t, db, "victim@x"); got.Up != 0 || got.Down != 0 || !got.Enable || got.ExpiryTime != 0 || got.Total != 0 {
				t.Fatalf("victim traffic up=%d down=%d enable=%v expiry=%d total=%d, want untouched by the node", got.Up, got.Down, got.Enable, got.ExpiryTime, got.Total)
			}
			if rec := readClientRecord(t, db, "victim@x"); rec.UUID != "11111111-1111-4111-8111-111111111111" || !rec.Enable || rec.ExpiryTime != 0 {
				t.Fatalf("victim record uuid=%q enable=%v expiry=%d, want the master's", rec.UUID, rec.Enable, rec.ExpiryTime)
			}
			if nodeHostsClient(t, db, "n1-in", "victim@x") {
				t.Fatal("the node inbound adopted a master-only client")
			}
		})
	}
}

// One entry the client model cannot decode (a string limitIp, an empty-string tgId) or
// without an email must not stop the inbound's other clients from being billed.
func TestOneOddNodeClientEntryKeepsTheOthersCredited(t *testing.T) {
	db := initTrafficTestDB(t)
	svc := &InboundService{}
	createNodeInbound(t, db, 1, "n1-in", 41001)
	settings := `{"clients":[{"email":"plain","enable":true},{"email":"legacy","limitIp":"2","tgId":"","enable":true},{"email":7},"junk"]}`
	stats := func(n int64) []xray.ClientTraffic {
		return []xray.ClientTraffic{{Email: "plain", Up: n, Down: n, Enable: true}, {Email: "legacy", Up: n, Down: n, Enable: true}}
	}
	// A parsable snapshot first: it creates the rows and the baselines.
	syncNode(t, svc, 1, "n1-in", stats(100)...)
	syncNodeWithSettings(t, svc, 1, "n1-in", settings, stats(150)...)
	assertUpDown(t, readTraffic(t, db, "plain"), 50, 50, "the readable client next to an odd entry")
	assertUpDown(t, readTraffic(t, db, "legacy"), 50, 50, "the client whose entry the model cannot decode")
}

// Stats alone attach nobody: an email in a node's ClientStats that its settings do not list
// is not on that inbound (a detach keeps the stat row), so it gets no traffic and no row.
func TestNodeStatsWithoutASettingsEntryCreditNothing(t *testing.T) {
	db := initTrafficTestDB(t)
	svc := &InboundService{}
	createNodeInbound(t, db, 1, "n1-in", 41001)
	for _, up := range []int64{100, 700} {
		syncNodeWithSettings(t, svc, 1, "n1-in", `{"clients":[{"email":"listed","enable":true}]}`,
			xray.ClientTraffic{Email: "listed", Up: up, Enable: true},
			xray.ClientTraffic{Email: "unlisted", Up: up, Enable: true})
	}
	assertUpDown(t, readTraffic(t, db, "listed"), 600, 0, "the listed client")
	var rows int64
	db.Model(xray.ClientTraffic{}).Where("email = ?", "unlisted").Count(&rows)
	if rows != 0 {
		t.Fatalf("traffic rows for a client the settings do not list = %d, want none", rows)
	}
}

// The same email on the master's own inbound and, separately, on a node's inbound is two
// clients: summing by email alone, as before #101, let any node bill any client.
func TestSameEmailOnMasterAndNodeIsNotSummedAcrossTiers(t *testing.T) {
	db := initTrafficTestDB(t)
	svc := &InboundService{}
	seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
	master := seedMasterInbound(t, db, "master-in", 40001, `{"email":"both@x","id":"11111111-1111-4111-8111-111111111111","enable":true}`)
	if err := db.Create(&xray.ClientTraffic{InboundId: master.Id, Email: "both@x", Enable: true, Up: 10, Down: 20}).Error; err != nil {
		t.Fatalf("seed traffic: %v", err)
	}
	createNodeInbound(t, db, 1, "n1-in", 41001)
	for _, up := range []int64{0, 100} {
		syncNodeWithSettings(t, svc, 1, "n1-in", `{"clients":[{"email":"both@x","enable":true}]}`,
			xray.ClientTraffic{Email: "both@x", Up: up, Down: up, Enable: true})
	}
	assertUpDown(t, readTraffic(t, db, "both@x"), 10, 20, "the master's own usage, without the node's")
}
