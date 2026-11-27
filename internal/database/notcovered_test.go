package database

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
)

// Tests for database code no test reached before (gremlins: NOT COVERED).

func epStream(entries ...string) string {
	return `{"externalProxy":[` + strings.Join(entries, ",") + `]}`
}

func ep(dest string) string {
	return `{"dest":"` + dest + `","port":443,"forceTls":"tls","remark":"` + dest + `"}`
}

func inboundHosts(t *testing.T, inboundID int) []model.Host {
	t.Helper()
	var hosts []model.Host
	if err := db.Where("inbound_id = ?", inboundID).Order("sort_order asc, id asc").Find(&hosts).Error; err != nil {
		t.Fatal(err)
	}
	return hosts
}

func hostAddrs(hosts []model.Host) string {
	addrs := make([]string, 0, len(hosts))
	for _, h := range hosts {
		addrs = append(addrs, h.Address)
	}
	return strings.Join(addrs, ",")
}

func TestSyncHostsFromExternalProxy(t *testing.T) {
	initTestDB(t)
	const ib = 7
	old := epStream(ep("a.com"), ep("b.com"))
	if _, err := CreateHostsFromExternalProxy(db, ib, old); err != nil {
		t.Fatal(err)
	}
	before := inboundHosts(t, ib)

	// No externalProxy key: hosts are left alone.
	if err := SyncHostsFromExternalProxy(db, ib, old, `{}`); err != nil || hostAddrs(inboundHosts(t, ib)) != "a.com,b.com" {
		t.Fatalf("no key: err=%v hosts=%s", err, hostAddrs(inboundHosts(t, ib)))
	}

	// b changed to c: the b row is rewritten in place.
	next := epStream(ep("a.com"), ep("c.com"))
	if err := SyncHostsFromExternalProxy(db, ib, old, next); err != nil {
		t.Fatal(err)
	}
	after := inboundHosts(t, ib)
	if hostAddrs(after) != "a.com,c.com" || after[1].Id != before[1].Id || after[1].GroupId != before[1].GroupId {
		t.Fatalf("change: %s, ids %d/%d", hostAddrs(after), after[1].Id, before[1].Id)
	}

	// Two entries go and one comes: the first gone row takes it, the second is deleted.
	last := epStream(ep("d.com"))
	if err := SyncHostsFromExternalProxy(db, ib, next, last); err != nil {
		t.Fatal(err)
	}
	if got := inboundHosts(t, ib); hostAddrs(got) != "d.com" || got[0].Id != before[0].Id {
		t.Fatalf("shrink: %s", hostAddrs(got))
	}

	// New entries beyond the gone ones are created.
	grown := epStream(ep("d.com"), ep("e.com"), ep("f.com"))
	if err := SyncHostsFromExternalProxy(db, ib, last, grown); err != nil || hostAddrs(inboundHosts(t, ib)) != "d.com,e.com,f.com" {
		t.Fatalf("grow: err=%v hosts=%s", err, hostAddrs(inboundHosts(t, ib)))
	}

	// Every surplus gone row is deleted, not just the first.
	if err := SyncHostsFromExternalProxy(db, ib, grown, epStream(ep("g.com"))); err != nil || hostAddrs(inboundHosts(t, ib)) != "g.com" {
		t.Fatalf("shrink by two: err=%v hosts=%s", err, hostAddrs(inboundHosts(t, ib)))
	}
}

func TestSyncHostsLeavesEditedAndSharedHosts(t *testing.T) {
	initTestDB(t)
	const ib = 8
	old := epStream(ep("a.com"), ep("b.com"))
	if _, err := CreateHostsFromExternalProxy(db, ib, old); err != nil {
		t.Fatal(err)
	}
	hosts := inboundHosts(t, ib)
	// The admin edited a, and put b in a group with another host.
	mustExec(t, "UPDATE hosts SET remark = 'mine' WHERE id = ?", hosts[0].Id)
	if err := db.Create(&model.Host{InboundId: 99, GroupId: hosts[1].GroupId, Remark: "x", Address: "x.com"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := SyncHostsFromExternalProxy(db, ib, old, epStream(ep("c.com"), ep("d.com"))); err != nil {
		t.Fatal(err)
	}
	if got := hostAddrs(inboundHosts(t, ib)); got != "a.com,b.com" {
		t.Fatalf("hosts = %s, want the edited and shared rows untouched", got)
	}
	// Only the shared row stays put when a is untouched again.
	mustExec(t, "UPDATE hosts SET remark = 'a.com' WHERE id = ?", hosts[0].Id)
	if err := SyncHostsFromExternalProxy(db, ib, old, epStream(ep("c.com"), ep("d.com"))); err != nil {
		t.Fatal(err)
	}
	// d takes b's slot, and b belongs to the admin now: d is not added.
	if got := hostAddrs(inboundHosts(t, ib)); got != "c.com,b.com" {
		t.Fatalf("hosts = %s", got)
	}
}

func TestSyncHostsWithoutChangesNeedsNoQuery(t *testing.T) {
	initTestDB(t)
	mustExec(t, "DROP TABLE hosts")
	same := epStream(ep("a.com"))
	if err := SyncHostsFromExternalProxy(db, 1, same, same); err != nil {
		t.Fatalf("unchanged list touched the table: %v", err)
	}
	if err := SyncHostsFromExternalProxy(db, 1, same, epStream(ep("b.com"))); err == nil {
		t.Fatal("a failed host read must be returned")
	}
}

func TestChangedImports(t *testing.T) {
	a, b, c := externalProxyEntryToHost(1, 0, map[string]any{"dest": "a"}), externalProxyEntryToHost(1, 1, map[string]any{"dest": "b"}), externalProxyEntryToHost(1, 2, map[string]any{"dest": "c"})
	if gone, added := changedImports([]*model.Host{a}, []*model.Host{a}); len(gone) != 0 || len(added) != 0 {
		t.Fatalf("same list: gone=%v added=%d", gone, len(added))
	}
	gone, added := changedImports([]*model.Host{a, b}, []*model.Host{c})
	if len(gone) != 2 || gone[0] != 0 || gone[1] != 1 || len(added) != 1 || added[0] != c {
		t.Fatalf("replace: gone=%v added=%d", gone, len(added))
	}
}

func TestImportedHostHelpers(t *testing.T) {
	a, b := model.Host{Tags: []string{}}, model.Host{}
	if !sameImportedHost(&a, &b) {
		t.Error("an empty list must match nil")
	}
	a.Tags = []string{"x"}
	if sameImportedHost(&a, &b) {
		t.Error("a non-empty list must not match nil")
	}
	if h := externalProxyEntryToHost(1, 1, map[string]any{"remark": " "}); h.Remark != "imported 2" {
		t.Errorf("remark = %q", h.Remark)
	}
	if got := anyToNonEmptyStrings([]string{"a", "", "b"}); strings.Join(got, ",") != "a,b" {
		t.Errorf("strings = %v", got)
	}
}

func TestIsIgnorableDuplicateColumnErr(t *testing.T) {
	initTestDB(t)
	rec := &model.ClientRecord{}
	cases := []struct {
		err  error
		gdb  bool
		want bool
	}{
		{nil, true, false},
		{errors.New("duplicate column name: email"), true, true},
		{errors.New("duplicate column name: `email`"), true, true},
		{errors.New("duplicate column name: "), true, false},
		{errors.New("duplicate column name: nope"), true, false},
		{errors.New("duplicate column name: email"), false, false},
		{errors.New(`ERROR: column "email" of relation "clients" already exists`), true, true},
		{errors.New(`ERROR: column "nope" of relation "clients" already exists`), true, false},
		{errors.New(`ERROR: column "" of relation "clients" already exists`), true, false},
		{errors.New(`ERROR: column "email of relation clients already exists`), true, false},
		{errors.New(`ERROR: column "email" of relation "clients" already exists`), false, false},
		{errors.New(`ERROR: relation "clients" already exists`), true, false},
		{errors.New("disk full"), true, false},
	}
	for _, c := range cases {
		gdb := db
		if !c.gdb {
			gdb = nil
		}
		if got := isIgnorableDuplicateColumnErr(gdb, c.err, rec); got != c.want {
			t.Errorf("%v (db=%v) = %v, want %v", c.err, c.gdb, got, c.want)
		}
	}
}

func createSettingsInbound(t *testing.T, tag string, port int, settings string) *model.Inbound {
	t.Helper()
	ib := &model.Inbound{Port: port, Tag: tag, Protocol: model.VLESS, Settings: settings, StreamSettings: "{}", Sniffing: "{}"}
	if err := db.Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	return ib
}

func settingsOf(t *testing.T, id int) string {
	t.Helper()
	var ib model.Inbound
	if err := db.First(&ib, id).Error; err != nil {
		t.Fatal(err)
	}
	return ib.Settings
}

// Every fix touches the inbounds that need it, skips blank, broken and clean ones,
// and records its history after them all.
func TestInboundSettingsFixes(t *testing.T) {
	initTestDB(t)
	blank := createSettingsInbound(t, "blank", 41001, " ")
	broken := createSettingsInbound(t, "broken", 41002, "{")
	noClients := createSettingsInbound(t, "noclients", 41003, `{"clients":"x"}`)
	tg1 := createSettingsInbound(t, "tg1", 41004, `{"clients":[1,{"email":"a"},{"tgId":5},{"tgId":"1 2"},{"tgId":1.5}]}`)
	tg2 := createSettingsInbound(t, "tg2", 41005, `{"clients":[{"tgId":"bad"}]}`)
	db.Where("seeder_name = ?", "InboundClientTgIdFix2").Delete(&model.HistoryOfSeeders{})
	if err := normalizeInboundClientTgId(); err != nil {
		t.Fatal(err)
	}
	s1, s2 := settingsOf(t, tg1.Id), settingsOf(t, tg2.Id)
	if !strings.Contains(s1, `"tgId": 5`) || !strings.Contains(s1, `"tgId": 12`) || !strings.Contains(s1, `"tgId": 0`) || !strings.Contains(s2, `"tgId": 0`) {
		t.Fatalf("tgId fix: %s / %s", s1, s2)
	}
	for _, ib := range []*model.Inbound{blank, broken, noClients} {
		if settingsOf(t, ib.Id) != ib.Settings {
			t.Errorf("inbound %s changed", ib.Tag)
		}
	}
	if !historyRecorded(t, "InboundClientTgIdFix2") {
		t.Error("tgId history missing")
	}

	nul1 := createSettingsInbound(t, "nul1", 41006, `{"clients":null}`)
	nul2 := createSettingsInbound(t, "nul2", 41007, `{"clients":null,"x":1}`)
	none := createSettingsInbound(t, "none", 41008, `{"x":1}`)
	db.Where("seeder_name = ?", "InboundClientsArrayFix").Delete(&model.HistoryOfSeeders{})
	if err := normalizeInboundClientsArray(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(settingsOf(t, nul1.Id), `"clients": []`) || !strings.Contains(settingsOf(t, nul2.Id), `"clients": []`) ||
		settingsOf(t, none.Id) != none.Settings || !historyRecorded(t, "InboundClientsArrayFix") {
		t.Fatalf("clients array fix: %s / %s / %s", settingsOf(t, nul1.Id), settingsOf(t, nul2.Id), settingsOf(t, none.Id))
	}
}

func TestResetIpLimitsWithoutFail2ban(t *testing.T) {
	initTestDB(t)
	t.Setenv("PATH", t.TempDir()) // no fail2ban-client: limits cannot be enforced
	db.Where("seeder_name = ?", "ResetIpLimitNoFail2ban").Delete(&model.HistoryOfSeeders{})
	createSettingsInbound(t, "blank", 41101, "")
	createSettingsInbound(t, "broken", 41102, "{")
	keep := createSettingsInbound(t, "keep", 41103, `{"clients":[1,{"email":"a"},{"limitIp":0}]}`)
	l1 := createSettingsInbound(t, "l1", 41104, `{"clients":[{"limitIp":3}]}`)
	l2 := createSettingsInbound(t, "l2", 41105, `{"clients":[{"limitIp":"2"}]}`)
	if err := db.Create(&model.ClientRecord{Email: "r@x", LimitIP: 4}).Error; err != nil {
		t.Fatal(err)
	}
	if err := resetIpLimitsWithoutFail2ban(); err != nil {
		t.Fatal(err)
	}
	if settingsOf(t, keep.Id) != keep.Settings || !strings.Contains(settingsOf(t, l1.Id), `"limitIp": 0`) || !strings.Contains(settingsOf(t, l2.Id), `"limitIp": 0`) {
		t.Fatalf("settings: %s / %s / %s", settingsOf(t, keep.Id), settingsOf(t, l1.Id), settingsOf(t, l2.Id))
	}
	var rec model.ClientRecord
	db.Where("email = ?", "r@x").First(&rec)
	if rec.LimitIP != 0 || !historyRecorded(t, "ResetIpLimitNoFail2ban") {
		t.Fatalf("client limit = %d, history = %v", rec.LimitIP, historyRecorded(t, "ResetIpLimitNoFail2ban"))
	}
}

func TestSeedWireguardPeersNeverReusesAnExistingEmail(t *testing.T) {
	initWGMigrationDB(t)
	if err := db.Create(&model.ClientRecord{Email: "wg-m-laptop", Comment: "kept"}).Error; err != nil {
		t.Fatal(err)
	}
	in := createWGInbound(t, "wg-m", 51824, []any{wgPeer("laptop", "priv-m", "pub-m", "10.0.3.2/32", 25), wgPeer("phone", "priv-n", "pub-n", "10.0.3.3/32", 25)})
	clearWGMigrationHistory(t)
	if err := seedWireguardPeersToClients(); err != nil {
		t.Fatal(err)
	}
	var kept model.ClientRecord
	db.Where("email = ?", "wg-m-laptop").First(&kept)
	var links int64
	db.Model(&model.ClientInbound{}).Where("inbound_id = ?", in.Id).Count(&links)
	if kept.Comment != "kept" || links != 2 {
		t.Fatalf("existing record = %+v, links = %d; want it untouched and both peers linked", kept, links)
	}
}

func TestRunSeedersHashesPlainPasswords(t *testing.T) {
	initTestDB(t)
	db.Where("seeder_name = ?", "UserPasswordHash").Delete(&model.HistoryOfSeeders{})
	mustExec(t, "INSERT INTO users (username, password) VALUES ('first', 'plain-one'), ('second', 'plain-two')")
	if err := runSeeders(false); err != nil {
		t.Fatal(err)
	}
	var users []model.User
	db.Find(&users)
	if len(users) < 2 {
		t.Fatalf("users = %d, want both", len(users))
	}
	for _, u := range users {
		if !crypto.IsHashed(u.Password) {
			t.Errorf("user %s password not hashed", u.Username)
		}
	}

	db.Where("seeder_name = ?", "UserPasswordHash").Delete(&model.HistoryOfSeeders{})
	mustExec(t, "UPDATE users SET password = ?", strings.Repeat("x", 73)) // bcrypt refuses over 72 bytes
	if err := runSeeders(false); err == nil {
		t.Fatal("a password bcrypt refuses must stop the seeders")
	}
}

func TestHashExistingApiTokensHashesEveryPlainToken(t *testing.T) {
	initTestDB(t)
	for _, tok := range []string{"plain-a", "plain-b"} {
		if err := db.Create(&model.ApiToken{Name: tok, Token: tok, Enabled: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Where("seeder_name = ?", "ApiTokensHash").Delete(&model.HistoryOfSeeders{})
	if err := hashExistingApiTokens(); err != nil {
		t.Fatal(err)
	}
	var rows []model.ApiToken
	db.Find(&rows)
	for _, r := range rows {
		if !crypto.IsSHA256Hex(r.Token) {
			t.Errorf("token %s not hashed", r.Name)
		}
	}
	if !historyRecorded(t, "ApiTokensHash") {
		t.Error("history missing")
	}
}

func TestStripRealityFinalmaskTcpOnEveryInbound(t *testing.T) {
	initTestDB(t)
	stream := `{"security":"reality","finalmask":{"tcp":[{"type":"sudoku"}]}}`
	var ids []int
	for i, port := range []int{41201, 41202} {
		ib := &model.Inbound{Port: port, Tag: "fm-" + itoa(i), Protocol: model.VLESS, Settings: "{}", StreamSettings: stream, Sniffing: "{}"}
		if err := db.Create(ib).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ib.Id)
	}
	db.Where("seeder_name = ?", "InboundRealityFinalmaskTcpStrip").Delete(&model.HistoryOfSeeders{})
	if err := stripRealityFinalmaskTcp(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		var ib model.Inbound
		db.First(&ib, id)
		if strings.Contains(ib.StreamSettings, "finalmask") {
			t.Errorf("inbound %d kept finalmask", id)
		}
	}
	if !historyRecorded(t, "InboundRealityFinalmaskTcpStrip") {
		t.Error("history missing")
	}
}

func TestDropLegacyInboundPortUniqueDropsEveryExplicitIndex(t *testing.T) {
	initTestDB(t)
	mustExec(t, "CREATE UNIQUE INDEX legacy_port_a ON inbounds(port)")
	mustExec(t, "CREATE UNIQUE INDEX legacy_port_b ON inbounds(port)")
	if err := dropLegacyInboundPortUnique(); err != nil {
		t.Fatal(err)
	}
	auto, explicit, err := sqliteUniquePortIndexes()
	if err != nil || len(auto)+len(explicit) != 0 {
		t.Fatalf("left: auto=%v explicit=%v err=%v", auto, explicit, err)
	}
}

func TestSQLiteFileHelpers(t *testing.T) {
	path := initTestDB(t)
	if d := Dialect(); d != DialectSQLite {
		t.Fatalf("Dialect() = %q", d)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if ok, err := IsSQLiteDB(f); !ok || err != nil {
		t.Fatalf("IsSQLiteDB(db) = %v, %v", ok, err)
	}
	if ok, err := IsSQLiteDB(strings.NewReader("not a database at all")); ok || err != nil {
		t.Fatalf("IsSQLiteDB(text) = %v, %v", ok, err)
	}
	if _, err := IsSQLiteDB(strings.NewReader("SQL")); err == nil {
		t.Fatal("a short file must be an error")
	}

	dir := t.TempDir()
	dst := filepath.Join(dir, "copy.db")
	if err := CopySQLiteFile(filepath.Join(dir, "missing.db"), dst); err == nil {
		t.Fatal("a missing source must fail")
	}
	if err := CopySQLiteFile(path, dst); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dst); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("copy: %v %v", st, err)
	}
	if err := CopySQLiteFile(path, dst); err == nil {
		t.Fatal("an existing destination must not be overwritten")
	}
	bad := filepath.Join(dir, "no-such-dir", "copy.db")
	if err := CopySQLiteFile(path, bad); err == nil {
		t.Fatal("VACUUM INTO a missing directory must fail")
	}
	if _, err := os.Lstat(bad); err == nil {
		t.Fatal("a failed copy left a file")
	}
}

func TestDialectWithoutADatabase(t *testing.T) {
	saved := db
	db = nil
	t.Cleanup(func() { db = saved })
	if Dialect() != "" {
		t.Fatal("no database must give no dialect")
	}
}
