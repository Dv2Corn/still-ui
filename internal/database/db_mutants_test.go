package database

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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
