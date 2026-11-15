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

// A reader that trimmed Unicode white space first parsed settings the strict readers refused.
// Bytes that are not one JSON object leave the inbound out, for billing and the client sync.
func TestNodeCannotSlipSettingsPastTheClaimCheckBehindWhiteSpace(t *testing.T) {
	const hostile = `{"clients":[{"email":"victim@x","id":"99999999-9999-4999-8999-999999999999","subId":"stolen-sub","enable":false,"expiryTime":1}]}`
	for name, prefix := range map[string]string{"form feed": "\f", "vertical tab": "\v", "no-break space": "\u00a0", "next line": "\u0085", "byte order mark": "\ufeff"} {
		t.Run(name, func(t *testing.T) {
			db := initTrafficTestDB(t)
			svc := &InboundService{}
			seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
			master := seedMasterInbound(t, db, "master-in", 40001,
				`{"email":"victim@x","id":"11111111-1111-4111-8111-111111111111","subId":"victim-sub","enable":true}`)
			if err := db.Create(&xray.ClientTraffic{InboundId: master.Id, Email: "victim@x", Enable: true}).Error; err != nil {
				t.Fatalf("seed traffic: %v", err)
			}
			createNodeInbound(t, db, 1, "n1-in", 41001)
			for _, settings := range []string{prefix + hostile, hostile + prefix} {
				for _, up := range []int64{0, 500} {
					syncNodeWithSettings(t, svc, 1, "n1-in", settings,
						xray.ClientTraffic{Email: "victim@x", Up: up, Down: up, Enable: false, ExpiryTime: 1})
				}
			}
			if got := readTraffic(t, db, "victim@x"); got.Up != 0 || !got.Enable || got.ExpiryTime != 0 {
				t.Fatalf("victim traffic up=%d enable=%v expiry=%d, want untouched by the node", got.Up, got.Enable, got.ExpiryTime)
			}
			if rec := readClientRecord(t, db, "victim@x"); rec.UUID != "11111111-1111-4111-8111-111111111111" || rec.SubID != "victim-sub" || !rec.Enable {
				t.Fatalf("victim record uuid=%q subId=%q enable=%v, want the master's", rec.UUID, rec.SubID, rec.Enable)
			}
			if nodeHostsClient(t, db, "n1-in", "victim@x") {
				t.Fatal("the node inbound adopted a master-only client")
			}
			if stored := storedInboundSettings(t, db, "n1-in"); stored != "" {
				t.Fatalf("the master stored the unreadable settings: %q", stored)
			}
		})
	}
}

// Unreadable settings leave the inbound out of every pass, the reported-email set too: its
// stats must not keep a baseline alive or reach a traffic row through a sibling.
func TestUnreadableNodeSettingsLeaveTheInboundOutOfEveryPass(t *testing.T) {
	db := initTrafficTestDB(t)
	svc := &InboundService{}
	createNodeInbound(t, db, 1, "n1-in", 41001)
	syncNode(t, svc, 1, "n1-in", xray.ClientTraffic{Email: "solo", Up: 100, Enable: true})
	before := readTraffic(t, db, "solo")
	syncNodeWithSettings(t, svc, 1, "n1-in", "\f"+`{"clients":[{"email":"solo"},{"email":"ghost"}]}`,
		xray.ClientTraffic{Email: "solo", Up: 900, Enable: true}, xray.ClientTraffic{Email: "ghost", Up: 900, Enable: true})
	if got := readTraffic(t, db, "solo"); got.Up != before.Up {
		t.Fatalf("solo up = %d after unreadable settings, want %d", got.Up, before.Up)
	}
	var rows int64
	db.Model(xray.ClientTraffic{}).Where("email = ?", "ghost").Count(&rows)
	if rows != 0 {
		t.Fatalf("a client from unreadable settings got %d traffic rows", rows)
	}
	syncNode(t, svc, 1, "n1-in", xray.ClientTraffic{Email: "solo", Up: 150, Enable: true})
	if got := readTraffic(t, db, "solo"); got.Up != before.Up+50 {
		t.Fatalf("solo up = %d after a readable snapshot, want %d: the skipped one must not move the baseline", got.Up, before.Up+50)
	}
}
