package service

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/panelimport"
	"github.com/mhsanaei/3x-ui/v3/internal/panelimport/marzbantest"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const importTestPort = 21000

func marzbanPlan(t *testing.T) *panelimport.Plan {
	t.Helper()
	dbPath, cfg := marzbantest.Build(t, importTestPort)
	plan, err := panelimport.ReadMarzban(dbPath, cfg)
	if err != nil {
		t.Fatalf("ReadMarzban: %v", err)
	}
	return plan
}

// tableCounts is every table's row count, so a write anywhere shows up.
func tableCounts(t *testing.T) map[string]int64 {
	t.Helper()
	db := database.GetDB()
	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, table := range tables {
		var n int64
		if err := db.Table(table).Count(&n).Error; err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}

// subscriptions is every client's subscription id by email.
func subscriptions(t *testing.T) map[string]string {
	t.Helper()
	var recs []model.ClientRecord
	if err := database.GetDB().Find(&recs).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range recs {
		out[r.Email] = r.SubID
	}
	return out
}

func skippedTags(r *PanelImportReport) []string {
	var out []string
	for _, in := range r.Inbounds {
		if in.Skipped != "" {
			out = append(out, in.Tag)
		}
	}
	return out
}

func TestPanelImport_DryRunWritesNothing(t *testing.T) {
	setupConflictDB(t)
	before := tableCounts(t)

	report, err := (&PanelImportService{}).Run(marzbanPlan(t), PanelImportOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if after := tableCounts(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("a dry run wrote to the database:\nbefore %v\nafter  %v", before, after)
	}
	if entries, _ := os.ReadDir(config.GetBackupFolderPath()); len(entries) != 0 {
		t.Fatalf("a dry run took a backup: %v", entries)
	}
	if report.Applied || report.NotEmpty || report.Backup != "" {
		t.Errorf("report = applied %v notEmpty %v backup %q", report.Applied, report.NotEmpty, report.Backup)
	}
	if len(report.Inbounds) != 9 || len(skippedTags(report)) != 0 {
		t.Errorf("%d inbounds, skipped %q; want 9 and none", len(report.Inbounds), skippedTags(report))
	}
	// alice, alice.vmess, alice.ss, bob, carol, dave, erin, frank.
	if report.Clients != 8 || report.LegacySubLinks != 6 {
		t.Errorf("clients %d, legacy links %d; want 8 and 6", report.Clients, report.LegacySubLinks)
	}
	if !slices.Equal(report.Admins, []string{"root", "reseller"}) {
		t.Errorf("admins = %q", report.Admins)
	}
}

func TestPanelImport_Apply(t *testing.T) {
	setupConflictDB(t)
	report, err := (&PanelImportService{}).Run(marzbanPlan(t), PanelImportOptions{UserId: 1})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !report.Applied {
		t.Fatal("report does not say applied")
	}
	if st, err := os.Stat(report.Backup); err != nil || st.Size() == 0 {
		t.Fatalf("backup %q: %v", report.Backup, err)
	}
	db := database.GetDB()

	var inbounds []model.Inbound
	if err := db.Order("id").Find(&inbounds).Error; err != nil {
		t.Fatal(err)
	}
	if len(inbounds) != 9 || inbounds[0].Tag != marzbantest.TagReality || inbounds[0].Port != importTestPort+1 || inbounds[0].UserId != 1 {
		t.Fatalf("%d inbounds, first %+v", len(inbounds), inbounds[0])
	}

	var alice model.ClientRecord
	if err := db.Where("email = ?", "alice").Take(&alice).Error; err != nil {
		t.Fatal(err)
	}
	if alice.UUID != marzbantest.AliceVless || alice.Password != marzbantest.AliceTrojan || len(alice.SubID) != 16 ||
		alice.TotalGB != 10*marzbantest.GiB || alice.ExpiryTime != marzbantest.AliceExpire*1000 || !alice.Enable ||
		alice.Comment != "paid until 2100" || alice.TrafficReset != "monthly" || alice.TrafficResetDay != 4 {
		t.Errorf("alice = %+v", alice)
	}
	if alice.SubID == marzbantest.DerivableAliceSubID {
		t.Error("alice's subscription id can be computed from Marzban's secret and her name")
	}
	var onInbounds []string
	if err := db.Table("client_inbounds").Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id").
		Where("client_inbounds.client_id = ?", alice.Id).Order("inbounds.id").Pluck("inbounds.tag", &onInbounds).Error; err != nil {
		t.Fatal(err)
	}
	wantOn := []string{
		marzbantest.TagReality, marzbantest.TagVlessWS, marzbantest.TagVlessTLS,
		marzbantest.TagTrojanGRPC, marzbantest.TagHTTPUpgrade, marzbantest.TagXHTTP,
	}
	if !slices.Equal(onInbounds, wantOn) {
		t.Errorf("alice is on %q, want %q", onInbounds, wantOn)
	}
	for email, want := range map[string]string{"alice.vmess": marzbantest.AliceVmess, "frank": marzbantest.FrankVmess} {
		var rec model.ClientRecord
		if err := db.Where("email = ?", email).Take(&rec).Error; err != nil || rec.UUID != want {
			t.Errorf("%s uuid = %q (%v), want %q", email, rec.UUID, err, want)
		}
	}
	var ss, bob, frank model.ClientRecord
	db.Where("email = ?", "alice.ss").Take(&ss)
	db.Where("email = ?", "bob").Take(&bob)
	db.Where("email = ?", "frank").Take(&frank)
	if ss.Password != marzbantest.AliceSS || ss.SubID != alice.SubID || bob.SubID == alice.SubID {
		t.Errorf("alice.ss = %+v", ss)
	}
	if bob.Enable || bob.UUID != marzbantest.BobVless {
		t.Errorf("bob = %+v, want a disabled client with his id", bob)
	}

	var stat xray.ClientTraffic
	if err := db.Where("email = ?", "alice").Take(&stat).Error; err != nil {
		t.Fatal(err)
	}
	if stat.Up+stat.Down != marzbantest.GiB || stat.Total != 10*marzbantest.GiB || stat.ExpiryTime != marzbantest.AliceExpire*1000 {
		t.Errorf("alice traffic = %+v", stat)
	}

	var hosts []model.Host
	if err := db.Where("inbound_id = ?", inbounds[1].Id).Order("sort_order").Find(&hosts).Error; err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 3 || hosts[0].Address != "cdn.example.com" || hosts[0].HostHeader != "host.example.com" || hosts[0].GroupId == "" {
		t.Errorf("hosts of %s = %+v", marzbantest.TagVlessWS, hosts)
	}

	tokens := map[string]string{
		marzbantest.AliceToken:          alice.SubID,
		marzbantest.AliceJWT:            alice.SubID,
		marzbantest.FrankToken:          frank.SubID,
		marzbantest.FrankRevokedToken:   marzbantest.FrankRevokedToken,
		alice.SubID:                     alice.SubID,
		marzbantest.DerivableAliceSubID: marzbantest.DerivableAliceSubID,
	}
	for token, want := range tokens {
		got, legacy := ResolveLegacySubID(token)
		if got != want || legacy != (want != token) {
			t.Errorf("ResolveLegacySubID(%q) = %q, %v; want %q", token, got, legacy, want)
		}
	}
}

// A Marzban user that could not connect must not be able to after the import.
func TestPanelImport_XrayServesOnlyUsersMarzbanServed(t *testing.T) {
	setupConflictDB(t)
	if _, err := (&PanelImportService{}).Run(marzbanPlan(t), PanelImportOptions{}); err != nil {
		t.Fatalf("import: %v", err)
	}
	raw, _ := emittedClients(t, marzbantest.TagReality)
	list, _ := raw.([]any)
	var ids []string
	for _, c := range list {
		entry, _ := c.(map[string]any)
		id, _ := entry["id"].(string)
		ids = append(ids, id)
		if id == marzbantest.AliceVless && entry["flow"] != "xtls-rprx-vision" {
			t.Errorf("alice's flow in the Xray config = %v", entry["flow"])
		}
	}
	sort.Strings(ids)
	// alice is active and erin on hold; bob is disabled, carol out of traffic, dave expired.
	want := []string{marzbantest.AliceVless, marzbantest.ErinVless}
	if !slices.Equal(ids, want) {
		t.Fatalf("Xray clients on %s = %q, want %q", marzbantest.TagReality, ids, want)
	}
}

func TestPanelImport_FailureMidImportLeavesNothing(t *testing.T) {
	setupConflictDB(t)
	plan := marzbanPlan(t)
	// Passes every check against the empty panel, then collides with the plan's own
	// first inbound once that one is written.
	clash := *plan.Inbounds[0]
	clash.Tag, clash.ClientStats = "clashing port", nil
	plan.Inbounds = append(plan.Inbounds, &clash)
	before := tableCounts(t)

	report, err := (&PanelImportService{}).Run(plan, PanelImportOptions{})
	if err == nil || !strings.HasPrefix(err.Error(), `nothing imported: inbound "clashing port":`) {
		t.Fatalf("err = %v, want the write of the clashing inbound to fail", err)
	}
	if report.Applied {
		t.Error("report says applied")
	}
	if after := tableCounts(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("the failed import left rows behind:\nbefore %v\nafter  %v", before, after)
	}
	if got, legacy := ResolveLegacySubID(marzbantest.AliceToken); legacy {
		t.Errorf("the failed import left Marzban links accepted: %q", got)
	}
}

func TestPanelImport_SecondRun(t *testing.T) {
	setupConflictDB(t)
	svc := &PanelImportService{}
	if _, err := svc.Run(marzbanPlan(t), PanelImportOptions{}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	before, subsBefore := tableCounts(t), subscriptions(t)

	if _, err := svc.Run(marzbanPlan(t), PanelImportOptions{}); !errors.Is(err, ErrPanelNotEmpty) {
		t.Fatalf("second import without merge: %v, want ErrPanelNotEmpty", err)
	}
	report, err := svc.Run(marzbanPlan(t), PanelImportOptions{Merge: true})
	if err != nil {
		t.Fatalf("second import with merge: %v", err)
	}
	if len(skippedTags(report)) != 9 || report.Clients != 0 {
		t.Errorf("skipped %q, clients %d; want all 9 inbounds skipped and no client", skippedTags(report), report.Clients)
	}
	if report.Inbounds[0].Skipped != "an inbound with this tag already exists" {
		t.Errorf("skip reason = %q", report.Inbounds[0].Skipped)
	}
	if after := tableCounts(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("the repeated import changed the database:\nbefore %v\nafter  %v", before, after)
	}
	if after := subscriptions(t); !reflect.DeepEqual(subsBefore, after) {
		t.Fatalf("the repeated import changed subscriptions:\nbefore %v\nafter  %v", subsBefore, after)
	}
}

// A later import may bring an inbound the first one left out, but a client this panel
// already has stays exactly as the panel has it, whatever the source says about it now.
func TestPanelImport_LaterImportLeavesExistingClientsAlone(t *testing.T) {
	setupConflictDB(t)
	svc := &PanelImportService{}
	first := marzbanPlan(t)
	first.Inbounds = slices.DeleteFunc(first.Inbounds, func(in *model.Inbound) bool { return in.Tag == marzbantest.TagXHTTP })
	if _, err := svc.Run(first, PanelImportOptions{}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	db := database.GetDB()
	// Marzban still says alice is enabled with 10 GiB.
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", "alice").Updates(map[string]any{"enable": false, "total_gb": 1}).Error; err != nil {
		t.Fatal(err)
	}
	var before, after []model.ClientRecord
	db.Order("id").Find(&before)

	report, err := svc.Run(marzbanPlan(t), PanelImportOptions{Merge: true})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	for _, in := range report.Inbounds {
		if (in.Skipped == "") != (in.Tag == marzbantest.TagXHTTP) || in.Clients != 0 {
			t.Errorf("inbound %q: skipped %q, %d clients; want only %q imported, with no client", in.Tag, in.Skipped, in.Clients, marzbantest.TagXHTTP)
		}
	}
	if !slices.Contains(report.Notes, `inbound "`+marzbantest.TagXHTTP+`": client "alice" already exists here, skipped`) || report.LegacySubLinks != 0 {
		t.Errorf("legacy links %d, notes %q", report.LegacySubLinks, report.Notes)
	}
	db.Order("id").Find(&after)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the later import changed clients:\nbefore %+v\nafter  %+v", before, after)
	}
	if got, legacy := ResolveLegacySubID(marzbantest.AliceToken); !legacy || got != subscriptions(t)["alice"] {
		t.Errorf("alice's Marzban link now opens %q, want her subscription", got)
	}
}

func xuiInbound(tag string, port int, clients string) *model.Inbound {
	return &model.Inbound{
		Remark: tag, Enable: true, Port: port, Protocol: model.VLESS, Tag: tag,
		StreamSettings: `{"network":"tcp","security":"none"}`, Settings: `{"clients":[` + clients + `],"decryption":"none"}`,
	}
}

func TestPanelImport_SubscriptionIdsInsideOneSource(t *testing.T) {
	setupConflictDB(t)
	plan := &panelimport.Plan{Source: panelimport.SourceXUI, Inbounds: []*model.Inbound{
		xuiInbound("one", importTestPort+60, `{"id":"11111111-1111-4111-8111-111111111111","email":"ann-1","subId":"annsub0123456789"},`+
			`{"id":"33333333-3333-4333-8333-333333333333","email":"kim","subId":"kimsub0123456789"}`),
		// ann-2 is the x-ui way of one subscription on two inbounds; kim with another id is not the same kim.
		xuiInbound("two", importTestPort+61, `{"id":"22222222-2222-4222-8222-222222222222","email":"ann-2","subId":"annsub0123456789"},`+
			`{"id":"44444444-4444-4444-8444-444444444444","email":"kim","subId":"othersub01234567"}`),
	}}
	report, err := (&PanelImportService{}).Run(plan, PanelImportOptions{})
	if err != nil || len(skippedTags(report)) != 0 {
		t.Fatalf("import: %v, skipped %q", err, skippedTags(report))
	}
	wantNotes := []string{
		`inbound "two": client "kim" appears again in the source with another subscription id, skipped`,
		"subscription links that several clients share in the source stay shared here: 1",
	}
	if !slices.Equal(report.Notes, wantNotes) || report.Clients != 3 {
		t.Errorf("clients %d, notes %q; want 3 and %q", report.Clients, report.Notes, wantNotes)
	}
	want := map[string]string{"ann-1": "annsub0123456789", "ann-2": "annsub0123456789", "kim": "kimsub0123456789"}
	if got := subscriptions(t); !reflect.DeepEqual(got, want) {
		t.Errorf("subscriptions = %v, want %v", got, want)
	}
	var kim model.ClientRecord
	database.GetDB().Where("email = ?", "kim").Take(&kim)
	if kim.UUID != "33333333-3333-4333-8333-333333333333" {
		t.Errorf("kim's id = %q, want the first kim's", kim.UUID)
	}
}

func TestPanelImport_MergeSkipsWhatConflicts(t *testing.T) {
	setupConflictDB(t)
	existing := &model.Inbound{
		Tag: "mine", Enable: true, Listen: "0.0.0.0", Port: importTestPort + 1, Protocol: model.VLESS,
		StreamSettings: `{"network":"tcp","security":"none"}`,
		Settings:       `{"clients":[{"id":"99999999-9999-4999-8999-999999999999","email":"carol","subId":"mine-carol","enable":true}],"decryption":"none"}`,
	}
	if _, _, err := (&InboundService{}).AddInbound(existing); err != nil {
		t.Fatalf("seed inbound: %v", err)
	}

	dry, err := (&PanelImportService{}).Run(marzbanPlan(t), PanelImportOptions{DryRun: true})
	if err != nil || !dry.NotEmpty {
		t.Fatalf("dry run on a panel with inbounds: notEmpty %v, err %v", dry.NotEmpty, err)
	}
	report, err := (&PanelImportService{}).Run(marzbanPlan(t), PanelImportOptions{Merge: true})
	if err != nil {
		t.Fatalf("merge import: %v", err)
	}
	if got := skippedTags(report); !slices.Equal(got, []string{marzbantest.TagReality}) {
		t.Errorf("skipped %q, want only the inbound whose port is taken", got)
	}
	if !slices.Contains(report.Notes, `inbound "VLESS WS TLS": client "carol" already exists here, skipped`) {
		t.Errorf("no note about the existing client carol: %q", report.Notes)
	}
	var carol model.ClientRecord
	if err := database.GetDB().Where("email = ?", "carol").Take(&carol).Error; err != nil {
		t.Fatal(err)
	}
	if carol.UUID != "99999999-9999-4999-8999-999999999999" || carol.SubID != "mine-carol" {
		t.Errorf("the panel's own carol was overwritten: %+v", carol)
	}
	// carol stays this panel's client, so Marzban's carol gets no legacy link.
	if report.LegacySubLinks != 5 {
		t.Errorf("legacy links = %d, want 5", report.LegacySubLinks)
	}
}

func TestPanelImport_XUI(t *testing.T) {
	setupConflictDB(t)
	plan := &panelimport.Plan{Source: panelimport.SourceXUI, Inbounds: []*model.Inbound{{
		Remark: "old", Enable: true, Port: importTestPort + 50, Protocol: model.VMESS, Tag: "inbound-old",
		StreamSettings: `{"network":"tcp","security":"none"}`,
		Settings:       `{"clients":[{"id":"11111111-1111-4111-8111-111111111111","alterId":0,"email":"inbound-old-1"}]}`,
		ClientStats:    []xray.ClientTraffic{{Email: "inbound-old-1", Enable: true, Up: 7, Down: 8}},
	}}}
	report, err := (&PanelImportService{}).Run(plan, PanelImportOptions{})
	if err != nil || len(skippedTags(report)) != 0 {
		t.Fatalf("import: %v, skipped %q (%+v)", err, skippedTags(report), report.Inbounds)
	}
	var rec model.ClientRecord
	var stat xray.ClientTraffic
	db := database.GetDB()
	if err := db.Where("email = ?", "inbound-old-1").Take(&rec).Error; err != nil || rec.UUID != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("client = %+v (%v)", rec, err)
	}
	if err := db.Where("email = ?", "inbound-old-1").Take(&stat).Error; err != nil || stat.Up != 7 || stat.Down != 8 {
		t.Errorf("traffic = %+v (%v)", stat, err)
	}
	if report.LegacySubLinks != 0 {
		t.Errorf("an x-ui import reports legacy links: %d", report.LegacySubLinks)
	}
}
