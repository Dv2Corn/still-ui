package database

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// PostgreSQL paths, and the checks around them that need no server.

func TestMigrateAndExportArgumentChecks(t *testing.T) {
	dir := t.TempDir()
	if err := MigrateData(filepath.Join(dir, "missing.db"), "host=x"); err == nil || !strings.Contains(err.Error(), "source sqlite not found") {
		t.Errorf("missing source: %v", err)
	}
	src := filepath.Join(dir, "src.db")
	if err := os.WriteFile(src, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateData(src, ""); err == nil || err.Error() != "destination DSN is required" {
		t.Errorf("empty DSN: %v", err)
	}

	if err := ExportPostgresToSQLite("", filepath.Join(dir, "out.db")); err == nil || err.Error() != "source DSN is required" {
		t.Errorf("empty source DSN: %v", err)
	}
	if err := ExportPostgresToSQLite("host=x", filepath.Join(src, "out.db")); err == nil {
		t.Error("a destination under a file must fail")
	}
	busy := filepath.Join(dir, "busy")
	if err := os.MkdirAll(filepath.Join(busy, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ExportPostgresToSQLite("host=x", busy); err == nil {
		t.Error("a destination that is a non-empty directory must fail")
	}
}

func TestTruncatePostgresTablesWithNoModelsRunsNothing(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := truncatePostgresTables(gdb, nil); err != nil {
		t.Fatalf("no models must issue no statement: %v", err)
	}
}

func TestOpenPostgresWithRetry(t *testing.T) {
	saved, savedDelays := openPostgres, postgresRetryDelays
	t.Cleanup(func() { openPostgres, postgresRetryDelays = saved, savedDelays })
	postgresRetryDelays = []time.Duration{0, 0, 0}
	logs := captureLog(t)
	ok, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})

	calls := 0
	openPostgres = func(string, *gorm.Config) (*gorm.DB, error) {
		calls++
		return ok, nil
	}
	if conn, err := openPostgresWithRetry("dsn", nil); err != nil || conn != ok || calls != 1 || strings.Contains(logs.String(), "established") {
		t.Fatalf("first try: err=%v calls=%d log=%q", err, calls, logs.String())
	}

	calls = 0
	openPostgres = func(string, *gorm.Config) (*gorm.DB, error) {
		if calls++; calls < 2 {
			return nil, errors.New("refused")
		}
		return ok, nil
	}
	if _, err := openPostgresWithRetry("dsn", nil); err != nil || calls != 2 ||
		!strings.Contains(logs.String(), "attempt 1/3 failed: refused") || !strings.Contains(logs.String(), "established on attempt 2/3") {
		t.Fatalf("retry: err=%v calls=%d log=%q", err, calls, logs.String())
	}

	openPostgres = func(string, *gorm.Config) (*gorm.DB, error) { return nil, errors.New("down") }
	if _, err := openPostgresWithRetry("dsn", nil); err == nil || err.Error() != "postgres unreachable after 3 attempts: down" {
		t.Fatalf("unreachable: %v", err)
	}

	// InitDB refuses a postgres kind without a DSN and passes the open error on.
	t.Setenv("XUI_DB_TYPE", "postgres")
	t.Setenv("XUI_DB_DSN", "")
	if err := InitDB(""); err == nil || !strings.Contains(err.Error(), "XUI_DB_DSN is empty") {
		t.Fatalf("empty DSN: %v", err)
	}
	t.Setenv("XUI_DB_DSN", "host=x")
	if err := InitDB(""); err == nil || !strings.Contains(err.Error(), "down") {
		t.Fatalf("open error: %v", err)
	}
}

func TestPostgresDefaultRetryDelays(t *testing.T) {
	want := []time.Duration{0, 2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second}
	if !slices.Equal(postgresRetryDelays, want) {
		t.Fatalf("delays = %v", postgresRetryDelays)
	}
}

func TestMigrateDataAndExport_Postgres(t *testing.T) {
	dsn := pgSchemaDSN(t)
	srcPath := filepath.Join(t.TempDir(), "src.db")
	if err := InitDB(srcPath); err != nil {
		t.Fatal(err)
	}
	for i, port := range []int{42001, 42002} {
		if err := db.Create(&model.Inbound{Port: port, Tag: "pg-" + itoa(i), Protocol: model.VLESS, Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := CloseDB(); err != nil {
		t.Fatal(err)
	}

	if err := MigrateData(srcPath, dsn); err != nil {
		t.Fatalf("MigrateData: %v", err)
	}
	// A second run replaces the copy instead of failing on duplicate ids.
	if err := MigrateData(srcPath, dsn); err != nil {
		t.Fatalf("MigrateData again: %v", err)
	}
	pg, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	pg.Model(&model.Inbound{}).Count(&n)
	if n != 2 {
		t.Fatalf("inbounds in postgres = %d, want 2", n)
	}
	// The id sequence was moved past the copied rows.
	next := model.Inbound{Port: 42003, Tag: "pg-next", Protocol: model.VLESS, Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}
	if err := pg.Create(&next).Error; err != nil {
		t.Fatalf("insert after migration: %v", err)
	}

	out := filepath.Join(t.TempDir(), "sub", "out.db")
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ExportPostgresToSQLite(dsn, out); err != nil {
		t.Fatalf("export: %v", err)
	}
	lite, err := gorm.Open(sqlite.Open(out), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	lite.Model(&model.Inbound{}).Count(&n)
	if n != 3 {
		t.Fatalf("inbounds in the export = %d, want 3", n)
	}

	// Truncate empties every listed table.
	if err := truncatePostgresTables(pg, []any{&model.Inbound{}}); err != nil {
		t.Fatal(err)
	}
	pg.Model(&model.Inbound{}).Count(&n)
	if n != 0 {
		t.Fatalf("inbounds after truncate = %d", n)
	}
}

func TestInitDBOnPostgresResyncsSequences(t *testing.T) {
	usePostgres(t)
	mustExec(t, `INSERT INTO inbounds (id, port, tag, protocol, settings, stream_settings, sniffing) VALUES (100, 42101, 'pg-100', 'vless', '{}', '{}', '{}')`)
	resyncPostgresSequences(db, []any{&model.Inbound{}, &model.ClientInbound{}})
	ib := model.Inbound{Port: 42102, Tag: "pg-next", Protocol: model.VLESS, Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}
	if err := db.Create(&ib).Error; err != nil || ib.Id != 101 {
		t.Fatalf("next id = %d, err = %v; want 101", ib.Id, err)
	}
	if Dialect() != DialectPostgres {
		t.Fatalf("Dialect() = %q", Dialect())
	}
	// The legacy foreign key is dropped.
	mustExec(t, `ALTER TABLE client_traffics ADD CONSTRAINT fk_inbounds_client_stats FOREIGN KEY (inbound_id) REFERENCES inbounds(id)`)
	if err := dropLegacyForeignKeys(); err != nil {
		t.Fatal(err)
	}
	var fk int64
	db.Raw(`SELECT count(*) FROM information_schema.table_constraints WHERE table_schema = current_schema() AND constraint_name = 'fk_inbounds_client_stats'`).Scan(&fk)
	if fk != 0 {
		t.Fatal("fk_inbounds_client_stats survived")
	}
}

func TestMigrateHostVerifyPeerCertByNameColumnFromBoolean_Postgres(t *testing.T) {
	usePostgres(t)
	mustExec(t, `ALTER TABLE hosts ALTER COLUMN verify_peer_cert_by_name DROP DEFAULT`)
	mustExec(t, `ALTER TABLE hosts ALTER COLUMN verify_peer_cert_by_name TYPE boolean USING false`)
	mustExec(t, `ALTER TABLE hosts ALTER COLUMN verify_peer_cert_by_name SET DEFAULT true`)
	if err := migrateHostVerifyPeerCertByNameColumn(); err != nil {
		t.Fatal(err)
	}
	var dataType string
	db.Raw(`SELECT data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'hosts' AND column_name = 'verify_peer_cert_by_name'`).Scan(&dataType)
	if dataType != "text" {
		t.Fatalf("column type = %q, want text", dataType)
	}
	// Already text: nothing to do.
	if err := migrateHostVerifyPeerCertByNameColumn(); err != nil {
		t.Fatal(err)
	}
}
