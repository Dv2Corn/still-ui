package database

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Boundary tests written against surviving mutants reported by gremlins (nightly mutation run).

func initTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	return path
}

func TestIsPostgresWithoutAnOpenDatabase(t *testing.T) {
	saved := db
	db = nil
	defer func() { db = saved }()
	if IsPostgres() {
		t.Fatal("IsPostgres() = true for the default sqlite kind with no open database")
	}
}

func TestInitDBAppliesCacheAndMmapPragmas(t *testing.T) {
	initTestDB(t)
	var cache, mmap int64
	db.Raw("PRAGMA cache_size").Scan(&cache)
	db.Raw("PRAGMA mmap_size").Scan(&mmap)
	if cache != -32*1024 || mmap != 256*1024*1024 {
		t.Fatalf("cache_size=%d mmap_size=%d, want -32768 and 268435456", cache, mmap)
	}
}

func TestEnvIntRejectsZero(t *testing.T) {
	t.Setenv("XUI_TEST_ENV_INT", "0")
	if got := envInt("XUI_TEST_ENV_INT", 32); got != 32 {
		t.Fatalf("envInt(\"0\") = %d, want the default 32", got)
	}
}

func TestCleanupSQLiteBackupDirsRemovesEveryBackupDir(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{sqliteBackupDirPrefix + "a", sqliteBackupDirPrefix + "b", "keep"} {
		if err := os.MkdirAll(filepath.Join(dir, name, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupSQLiteBackupDirs(dir); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "keep" {
		t.Fatalf("left %v, want only keep", entries)
	}
}

func TestValidateSQLiteDB(t *testing.T) {
	dir := t.TempDir()
	if err := ValidateSQLiteDB(filepath.Join(dir, "missing.db")); err == nil {
		t.Error("a missing file must not validate")
	}
	junk := filepath.Join(dir, "junk.db")
	if err := os.WriteFile(junk, []byte("this is not a sqlite database at all, just text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSQLiteDB(junk); err == nil {
		t.Error("a non-sqlite file must not validate")
	}
	good := initTestDB(t)
	if err := CloseDB(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSQLiteDB(good); err != nil {
		t.Errorf("a fresh database must validate: %v", err)
	}
}

func TestCopyTableCopiesPastOneFullBatch(t *testing.T) {
	open := func(name string) *gorm.DB {
		g, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), name)), &gorm.Config{Logger: logger.Discard})
		if err != nil {
			t.Fatal(err)
		}
		if err := g.AutoMigrate(&model.Setting{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if s, err := g.DB(); err == nil {
				_ = s.Close()
			}
		})
		return g
	}
	src, dst := open("src.db"), open("dst.db")
	rows := make([]model.Setting, 501) // one full batch of 500 plus one
	for i := range rows {
		rows[i] = model.Setting{Key: "k" + itoa(i), Value: "v"}
	}
	if err := src.CreateInBatches(rows, 200).Error; err != nil {
		t.Fatal(err)
	}
	n, err := copyTable(src, dst, &model.Setting{})
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	dst.Model(&model.Setting{}).Count(&count)
	if n != 501 || count != 501 {
		t.Fatalf("copied n=%d, destination has %d rows, want 501", n, count)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestDedupeInboundSettingsClientsWithTwoClientsInEveryInbound(t *testing.T) {
	initTestDB(t)
	settings := `{"clients":[{"email":"a@x","id":"1"},{"email":"A@x","id":"2"}]}`
	for i, port := range []int{40101, 40102} {
		ib := &model.Inbound{Port: port, Tag: "dedupe-" + itoa(i), Protocol: "vless", Settings: settings, StreamSettings: "{}", Sniffing: "{}"}
		if err := db.Create(ib).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := dedupeInboundSettingsClients(); err != nil {
		t.Fatal(err)
	}
	var ibs []model.Inbound
	db.Where("tag LIKE ?", "dedupe-%").Find(&ibs)
	for _, ib := range ibs {
		var s struct {
			Clients []any `json:"clients"`
		}
		_ = json.Unmarshal([]byte(ib.Settings), &s)
		if len(s.Clients) != 1 {
			t.Errorf("inbound %s keeps %d clients, want 1", ib.Tag, len(s.Clients))
		}
	}
}

func TestCreateHostsFromExternalProxyCountsCreatedHosts(t *testing.T) {
	initTestDB(t)
	stream := `{"externalProxy":[{"forceTls":"same","dest":"a.example.com","port":443,"remark":"a"},{"forceTls":"same","dest":"b.example.com","port":443,"remark":"b"}]}`
	n, err := CreateHostsFromExternalProxy(db, 1, stream)
	if err != nil || n != 2 {
		t.Fatalf("CreateHostsFromExternalProxy = (%d, %v), want (2, nil)", n, err)
	}
}

// The fresh-install path migrates a legacy apiToken setting after the egress filter seed.
func TestFreshInstallMigratesTheLegacyAPIToken(t *testing.T) {
	initTestDB(t)
	for _, m := range []any{&model.HistoryOfSeeders{}, &model.User{}, &model.ApiToken{}} {
		if err := db.Where("1 = 1").Delete(m).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Where("key = ?", "apiToken").Delete(&model.Setting{})
	if err := db.Create(&model.Setting{Key: "apiToken", Value: "legacy-token"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := runSeeders(true); err != nil {
		t.Fatalf("runSeeders: %v", err)
	}
	var count int64
	db.Model(&model.ApiToken{}).Where("name = ?", "default").Count(&count)
	if count != 1 {
		t.Fatalf("api tokens named default = %d, want the legacy token migrated", count)
	}
	if !historyRecorded(t, "ApiTokensTable") {
		t.Fatal("seedApiTokens did not record its history row after migrating the legacy token")
	}
}

// A database whose index no longer matches its declared columns opens and reads fine, but
// integrity_check reports rows (not an error): the result text itself must be checked.
func TestValidateSQLiteDBDetectsAMismatchedIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mismatch.db")
	g, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)",
		"CREATE INDEX t_v ON t(v)",
		"INSERT INTO t (v) VALUES ('a'), ('b'), ('c')",
		"PRAGMA writable_schema=ON",
		"UPDATE sqlite_master SET sql = 'CREATE INDEX t_v ON t(id)' WHERE name = 't_v'",
	} {
		if err := g.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if s, err := g.DB(); err == nil {
		_ = s.Close()
	}
	if err := ValidateSQLiteDB(path); err == nil || !strings.Contains(err.Error(), "integrity check failed") {
		t.Fatalf("ValidateSQLiteDB = %v, want an integrity check failure", err)
	}
}

func TestDBConnMaxIdleTime(t *testing.T) {
	if dbConnMaxIdleTime != 30*time.Minute {
		t.Fatalf("dbConnMaxIdleTime = %v, want 30m", dbConnMaxIdleTime)
	}
}

func TestExternalProxyRemarkIsCappedAt256Bytes(t *testing.T) {
	for n, want := range map[int]int{255: 255, 256: 256, 300: 256} {
		h := externalProxyEntryToHost(1, 0, map[string]any{"remark": strings.Repeat("r", n)})
		if len(h.Remark) != want {
			t.Errorf("remark of %d bytes -> %d, want %d", n, len(h.Remark), want)
		}
	}
}

func TestLegacyInboundRebuildDropsItsScratchTable(t *testing.T) {
	dbPath := openLegacyPortUniqueDB(t)
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB over legacy schema: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	if db.Migrator().HasTable("inbounds_legacy_rebuild") {
		t.Fatal("the rebuild left inbounds_legacy_rebuild behind")
	}
}

func TestClientNullColumnsAreBackfilled(t *testing.T) {
	initTestDB(t)
	if err := db.Create(&model.ClientRecord{Email: "null@x", Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	mustExec(t, "UPDATE clients SET traffic_reset = NULL, traffic_reset_day = NULL, sync_orphaned_at = NULL, sync_orphaned_by = NULL")
	if err := migrateClientTrafficResetColumns(); err != nil {
		t.Fatal(err)
	}
	if err := migrateSyncOrphanColumns(); err != nil {
		t.Fatal(err)
	}
	var row struct {
		TrafficReset    *string
		TrafficResetDay *int
		SyncOrphanedAt  *int64
		SyncOrphanedBy  *int
	}
	db.Raw("SELECT traffic_reset, traffic_reset_day, sync_orphaned_at, sync_orphaned_by FROM clients WHERE email = ?", "null@x").Scan(&row)
	if row.TrafficReset == nil || *row.TrafficReset != "never" || row.TrafficResetDay == nil || *row.TrafficResetDay != 1 ||
		row.SyncOrphanedAt == nil || row.SyncOrphanedBy == nil {
		t.Fatalf("after backfill: %+v", row)
	}
}

func TestMtprotoCustomShareAddrMovesEveryInbound(t *testing.T) {
	initTestDB(t)
	db.Where("seeder_name = ?", "MtprotoCustomShareAddrToHosts").Delete(&model.HistoryOfSeeders{})
	for i, port := range []int{4061, 4062} {
		ib := &model.Inbound{
			UserId: 1, Tag: "mt-" + itoa(i), Enable: true, Port: port, Protocol: model.MTProto,
			Settings: `{"clients":[]}`, StreamSettings: `{}`, ShareAddrStrategy: "custom", ShareAddr: "p" + itoa(i) + ".example.com",
		}
		if err := db.Create(ib).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := seedMtprotoCustomShareAddrToHosts(); err != nil {
		t.Fatal(err)
	}
	var left int64
	db.Model(&model.Inbound{}).Where("tag LIKE ? AND share_addr_strategy = ?", "mt-%", "custom").Count(&left)
	var hist int64
	db.Model(&model.HistoryOfSeeders{}).Where("seeder_name = ?", "MtprotoCustomShareAddrToHosts").Count(&hist)
	if left != 0 || hist != 1 {
		t.Fatalf("inbounds still custom = %d, history rows = %d; want 0 and 1", left, hist)
	}
}

// With a rollback journal the backup copies 128 pages per step: a database larger than one
// step must still be copied whole.
func TestBackupSQLiteCopiesEveryStepInDeleteMode(t *testing.T) {
	t.Setenv("XUI_DB_JOURNAL_MODE", "DELETE")
	initTestDB(t)
	big := strings.Repeat("x", 400)
	rows := make([]model.Setting, 3000) // ~1.2 MB, far more than 128 pages
	for i := range rows {
		rows[i] = model.Setting{Key: "bulk-" + itoa(i), Value: big}
	}
	if err := db.CreateInBatches(rows, 500).Error; err != nil {
		t.Fatal(err)
	}
	var want int64
	db.Model(&model.Setting{}).Count(&want)
	dst := filepath.Join(t.TempDir(), "backup.db")
	if err := BackupSQLite(dst); err != nil {
		t.Fatal(err)
	}
	g, err := gorm.Open(sqlite.Open(dst), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s, err := g.DB(); err == nil {
			_ = s.Close()
		}
	}()
	var got int64
	if err := g.Model(&model.Setting{}).Count(&got).Error; err != nil || got != want {
		t.Fatalf("backup has %d settings (err %v), want %d", got, err, want)
	}
}

func TestFail2banStateHonoursTheDisableSwitch(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "fail2ban-client"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("XUI_ENABLE_FAIL2BAN", "false")
	if st, err := fail2banEnforcementState(); err != nil || st != fail2banAbsent {
		t.Fatalf("disabled: state = %v (err %v), want absent", st, err)
	}
	t.Setenv("XUI_ENABLE_FAIL2BAN", "true")
	if st, err := fail2banEnforcementState(); err != nil || st != fail2banEnforcing {
		t.Fatalf("enabled with fail2ban-client present: state = %v (err %v), want enforcing", st, err)
	}
}

type noPKRow struct {
	Name  string
	Value string
}

func TestCopyTableWithoutAPrimaryKey(t *testing.T) {
	open := func(name string) *gorm.DB {
		g, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), name)), &gorm.Config{Logger: logger.Discard})
		if err != nil {
			t.Fatal(err)
		}
		if err := g.AutoMigrate(&noPKRow{}); err != nil {
			t.Fatal(err)
		}
		return g
	}
	src, dst := open("src.db"), open("dst.db")
	if err := src.Create(&[]noPKRow{{"a", "1"}, {"b", "2"}}).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := copyTable(src, dst, &noPKRow{}); err != nil || n != 2 {
		t.Fatalf("copyTable = (%d, %v), want (2, nil)", n, err)
	}
}

// The legacy table has two single-column UNIQUE indexes, on port and on tag: only the port one
// may be reported for dropping.
func TestSQLiteUniquePortIndexesFindsOnlyThePortIndex(t *testing.T) {
	path := openLegacyPortUniqueDB(t)
	saved := db
	t.Cleanup(func() { db = saved })
	g, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s, err := g.DB(); err == nil {
			_ = s.Close()
		}
	})
	db = g
	auto, explicit, err := sqliteUniquePortIndexes()
	if err != nil || len(auto) != 1 || len(explicit) != 0 {
		t.Fatalf("auto=%v explicit=%v err=%v, want one automatic index", auto, explicit, err)
	}
	var cols []struct{ Name string }
	db.Raw(`PRAGMA index_info("` + auto[0] + `")`).Scan(&cols)
	if len(cols) != 1 || cols[0].Name != "port" {
		t.Fatalf("reported index %s covers %v, want port", auto[0], cols)
	}
}
