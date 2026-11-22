package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func assertAuditLogSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	if !db.Migrator().HasTable(&model.AuditLog{}) {
		t.Fatalf("audit_logs table missing")
	}
	for _, col := range []string{"time", "actor", "ip", "action", "target", "outcome", "detail", "prev_hash", "hash"} {
		if !db.Migrator().HasColumn(&model.AuditLog{}, col) {
			t.Fatalf("audit_logs.%s missing", col)
		}
	}
}

// A panel upgraded from a release without the audit log gets the table on its next start.
func TestAuditLogTableCreatedOnUpgrade(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	path := filepath.Join(dbDir, "x-ui.db")
	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	if err := GetDB().Migrator().DropTable(&model.AuditLog{}); err != nil {
		t.Fatalf("drop audit_logs to model the older release: %v", err)
	}
	if err := CloseDB(); err != nil {
		t.Fatalf("CloseDB: %v", err)
	}
	if err := InitDB(path); err != nil {
		t.Fatalf("reopen InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	assertAuditLogSchema(t, GetDB())
}

func TestAuditLogSchemaPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("XUI_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("set XUI_TEST_PG_DSN to a reachable Postgres to run this test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("postgres db handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("automigrate postgres: %v", err)
	}
	assertAuditLogSchema(t, db)
}
