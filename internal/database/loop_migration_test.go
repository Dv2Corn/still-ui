package database

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A migration that returned early after its first successful write would silently skip the
// remaining rows and its history row: these run every loop over two rows.

func historyRecorded(t *testing.T, name string) bool {
	t.Helper()
	var n int64
	db.Model(&model.HistoryOfSeeders{}).Where("seeder_name = ?", name).Count(&n)
	return n == 1
}

func TestSeedWireguardPeersToClientsCoversEveryInbound(t *testing.T) {
	initWGMigrationDB(t)
	createWGInbound(t, "wg-a", 51821, []any{wgPeer("laptop", "priv-a", "pub-a", "10.0.0.2/32", 25)})
	createWGInbound(t, "wg-b", 51822, []any{wgPeer("laptop", "priv-b", "pub-b", "10.0.1.2/32", 25)})
	clearWGMigrationHistory(t)
	if err := seedWireguardPeersToClients(); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Model(&model.ClientRecord{}).Where("email IN ?", []string{"wg-a-laptop", "wg-b-laptop"}).Count(&n)
	if n != 2 || !historyRecorded(t, "WireguardPeersToClients") {
		t.Fatalf("clients migrated = %d, history recorded = %v; want 2 and true", n, historyRecorded(t, "WireguardPeersToClients"))
	}
}

func TestSeedMtprotoSecretsToClientsCoversEveryInbound(t *testing.T) {
	initMtprotoMigrationDB(t)
	const secret = "eedeadbeefdeadbeefdeadbeefdeadbe7777772e636c6f7564666c6172652e636f6d"
	a := createMtprotoInbound(t, "mt-a", 8446, map[string]any{"fakeTlsDomain": "www.cloudflare.com", "secret": secret})
	b := createMtprotoInbound(t, "mt-b", 8447, map[string]any{"fakeTlsDomain": "www.cloudflare.com", "secret": secret, "clients": []any{}})
	clearSeederHistory(t, "MtprotoSecretsToClients")
	if err := seedMtprotoSecretsToClients(); err != nil {
		t.Fatal(err)
	}
	for _, ib := range []*model.Inbound{a, b} {
		clients, _ := reloadInboundSettings(t, ib.Id)["clients"].([]any)
		if len(clients) != 1 {
			t.Errorf("inbound %s: clients = %v, want the legacy secret moved to one client", ib.Tag, clients)
		}
	}
	if !historyRecorded(t, "MtprotoSecretsToClients") {
		t.Error("history row missing")
	}
}

func TestMigrateShadowsocks2022ChachaCoversEveryInbound(t *testing.T) {
	initTestDB(t)
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	settings := `{"method":"2022-blake3-chacha20-poly1305","password":"` + key + `","clients":[{"email":"c","password":"` + key + `"}]}`
	for i, port := range []int{31301, 31302} {
		ib := model.Inbound{UserId: 1, Port: port, Protocol: model.Shadowsocks, Tag: "ss-" + itoa(i), Settings: settings}
		if err := db.Create(&ib).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateShadowsocks2022ChachaInbounds(); err != nil {
		t.Fatal(err)
	}
	var ibs []model.Inbound
	db.Where("tag LIKE ?", "ss-%").Find(&ibs)
	for _, ib := range ibs {
		var s struct{ Method string }
		_ = json.Unmarshal([]byte(ib.Settings), &s)
		if s.Method != "2022-blake3-aes-256-gcm" {
			t.Errorf("inbound %s method = %q, want 2022-blake3-aes-256-gcm", ib.Tag, s.Method)
		}
	}
}

func TestBackfillEmptyHostGroupIdsCoversEveryHost(t *testing.T) {
	initTestDB(t)
	for i := 0; i < 2; i++ {
		if err := db.Create(&model.Host{InboundId: 1, Remark: "legacy" + itoa(i), Address: "c.cdn.com", Port: 443, Security: "tls"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := backfillEmptyHostGroupIds(); err != nil {
		t.Fatal(err)
	}
	var empty int64
	db.Model(&model.Host{}).Where("group_id = '' OR group_id IS NULL").Count(&empty)
	if empty != 0 {
		t.Fatalf("%d hosts still have no group id", empty)
	}
}

func TestBackfillClientForwardedPortsCoversEveryClient(t *testing.T) {
	initTestDB(t)
	for _, e := range []string{"fa@x", "fb@x"} {
		if err := db.Create(&model.ClientRecord{Email: e, Enable: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ib := model.Inbound{
		UserId: 1, Port: 51830, Protocol: model.AmneziaWG, Tag: "awg-fp",
		Settings: `{"clients":[{"email":"fa@x","forwardedPorts":"8080"},{"email":"fb@x","forwardedPorts":"9090"}]}`,
	}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatal(err)
	}
	db.Where("seeder_name = ?", "ClientForwardedPortsBackfill").Delete(&model.HistoryOfSeeders{})
	if err := backfillClientForwardedPorts(); err != nil {
		t.Fatal(err)
	}
	var filled int64
	db.Model(&model.ClientRecord{}).Where("email IN ? AND wg_forwarded_ports <> ''", []string{"fa@x", "fb@x"}).Count(&filled)
	if filled != 2 || !historyRecorded(t, "ClientForwardedPortsBackfill") {
		t.Fatalf("filled = %d, history = %v; want 2 and true", filled, historyRecorded(t, "ClientForwardedPortsBackfill"))
	}
}

func TestTemplateRewritesRecordHistoryAfterChanging(t *testing.T) {
	initTestDB(t)
	stored := `{"outbounds":[{"protocol":"dns","tag":"dns-out","settings":{"rules":[{"action":"drop","qType":0},{"action":"hijack","qType":"1,28"},{"action":"drop"}]}}]}`
	seedDNSOutboundTemplate(t, stored)
	db.Where("seeder_name = ?", "DNSOutboundQTypeZeroFix").Delete(&model.HistoryOfSeeders{})
	if err := migrateDNSOutboundQTypeZero(); err != nil {
		t.Fatal(err)
	}
	if storedDNSOutboundTemplate(t) == stored || !historyRecorded(t, "DNSOutboundQTypeZeroFix") {
		t.Fatalf("dns qType fix: changed = %v, history = %v", storedDNSOutboundTemplate(t) != stored, historyRecorded(t, "DNSOutboundQTypeZeroFix"))
	}

	upper := `{"outbounds":[{"protocol":"Freedom","tag":"direct","settings":{}}]}`
	seedDNSOutboundTemplate(t, upper)
	db.Where("seeder_name = ?", "UppercaseFreedomFinalRulesFix").Delete(&model.HistoryOfSeeders{})
	if err := fixUppercaseFreedomFinalRules(); err != nil {
		t.Fatal(err)
	}
	if storedDNSOutboundTemplate(t) == upper || !historyRecorded(t, "UppercaseFreedomFinalRulesFix") {
		t.Fatalf("uppercase freedom fix: changed = %v, history = %v", storedDNSOutboundTemplate(t) != upper, historyRecorded(t, "UppercaseFreedomFinalRulesFix"))
	}
}

func TestSeedWireguardPeersKeepsAGivenSubIDAndFillsAMissingOne(t *testing.T) {
	initWGMigrationDB(t)
	withSub := wgPeer("phone", "priv-p", "pub-p", "10.0.2.2/32", 0).(map[string]any)
	withSub["subId"] = "keep-me-sub-id01"
	createWGInbound(t, "wg-s", 51823, []any{withSub, wgPeer("tablet", "priv-t", "pub-t", "10.0.2.3/32", 0)})
	clearWGMigrationHistory(t)
	if err := seedWireguardPeersToClients(); err != nil {
		t.Fatal(err)
	}
	var phone, tablet model.ClientRecord
	db.Where("email = ?", "wg-s-phone").First(&phone)
	db.Where("email = ?", "wg-s-tablet").First(&tablet)
	if phone.SubID != "keep-me-sub-id01" || tablet.SubID == "" {
		t.Fatalf("subIds = %q / %q, want the given one kept and the missing one generated", phone.SubID, tablet.SubID)
	}
}

func TestSeedClientsFromInboundJSONMergesEveryExistingClient(t *testing.T) {
	initTestDB(t)
	settings := `{"clients":[{"id":"u-a","email":"a@example.com","enable":true,"comment":"from-json"},{"id":"u-b","email":"b@example.com","enable":true,"comment":"from-json"}]}`
	ib := model.Inbound{UserId: 1, Port: 12346, Protocol: model.VLESS, Settings: settings, Tag: "seed-json"}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"a@example.com", "b@example.com"} {
		if err := db.Create(&model.ClientRecord{Email: e, Enable: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Where("seeder_name = ?", "ClientsTable").Delete(&model.HistoryOfSeeders{})
	if err := seedClientsFromInboundJSON(); err != nil {
		t.Fatal(err)
	}
	var merged, links int64
	db.Model(&model.ClientRecord{}).Where("email IN ? AND comment = ?", []string{"a@example.com", "b@example.com"}, "from-json").Count(&merged)
	db.Model(&model.ClientInbound{}).Where("inbound_id = ?", ib.Id).Count(&links)
	if merged != 2 || links != 2 {
		t.Fatalf("merged = %d, links = %d; want both clients merged and linked", merged, links)
	}
}

func TestMigrateApiTokenScopeAndExpiryInOneRun(t *testing.T) {
	originalDB := db
	t.Cleanup(func() { db = originalDB })
	var err error
	db, err = gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	// Scope exists but is empty on an old row; ExpiresAt is missing.
	mustExec(t, `CREATE TABLE api_tokens (id integer primary key autoincrement, name text, token text, enabled numeric, created_at integer, scope text)`)
	mustExec(t, "INSERT INTO api_tokens(name, token, enabled, created_at, scope) VALUES ('legacy','hash',1,1,'')")
	if err := migrateApiTokenScopeAndExpiry(); err != nil {
		t.Fatal(err)
	}
	var row model.ApiToken
	if err := db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(&model.ApiToken{}, "ExpiresAt") || row.Scope != model.ApiScopeAdmin {
		t.Fatalf("after one run: expires_at column = %v, scope = %q", db.Migrator().HasColumn(&model.ApiToken{}, "ExpiresAt"), row.Scope)
	}

	// Neither column exists yet: one run adds both.
	db, err = gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, `CREATE TABLE api_tokens (id integer primary key autoincrement, name text, token text, enabled numeric, created_at integer)`)
	if err := migrateApiTokenScopeAndExpiry(); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(&model.ApiToken{}, "Scope") || !db.Migrator().HasColumn(&model.ApiToken{}, "ExpiresAt") {
		t.Fatal("one run must add both scope and expires_at")
	}
}
