package database

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A node registered before the traffic quota existed must come out of the upgrade with
// no limit and a 1.0 multiplier: a zero multiplier would stop billing its clients.
func assertLegacyNodeTrafficDefaults(t *testing.T) {
	t.Helper()
	var n model.Node
	if err := GetDB().Where("name = ?", "legacy-node").First(&n).Error; err != nil {
		t.Fatalf("read legacy node: %v", err)
	}
	got := fmt.Sprintf("limit=%d day=%d warn=%d action=%s mult=%v used=%d start=%d limited=%v level=%d",
		n.TrafficLimitBytes, n.TrafficResetDay, n.TrafficWarnPercent, n.TrafficLimitAction,
		n.TrafficMultiplier, n.TrafficUsed, n.TrafficPeriodStart, n.TrafficLimited, n.TrafficAlertLevel)
	want := "limit=0 day=1 warn=80 action=notify mult=1 used=0 start=0 limited=false level=0"
	if got != want {
		t.Fatalf("legacy node after migration: %s, want %s", got, want)
	}
	if err := GetDB().Model(&model.Node{}).Where("id = ?", n.Id).
		Update("traffic_disabled_inbounds", "[3,4]").Error; err != nil {
		t.Fatalf("write disabled inbounds: %v", err)
	}
	if err := GetDB().First(&n, n.Id).Error; err != nil || len(n.TrafficDisabledInbounds) != 2 {
		t.Fatalf("disabled inbounds round trip = %v, %v", n.TrafficDisabledInbounds, err)
	}
}

func TestNodeTrafficLimitMigrationSQLite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	path := filepath.Join(dir, "x-ui.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`CREATE TABLE nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, address TEXT, port INTEGER, api_token TEXT, enable NUMERIC DEFAULT true, status TEXT DEFAULT 'unknown')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`INSERT INTO nodes (name, address, port, api_token) VALUES ('legacy-node', '10.0.0.1', 2053, 'tok')`).Error; err != nil {
		t.Fatal(err)
	}
	if h, err := legacy.DB(); err == nil {
		_ = h.Close()
	}
	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB legacy schema: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	assertLegacyNodeTrafficDefaults(t)
}

// A quota column an older SQLite ALTER TABLE added without its default holds NULL on the
// rows it found; the backfill gives them the defaults instead of a 0 multiplier.
func TestNodeTrafficLimitNullColumnsBackfilledSQLite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	path := filepath.Join(dir, "x-ui.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`CREATE TABLE nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, address TEXT, port INTEGER, api_token TEXT, enable NUMERIC DEFAULT true, status TEXT DEFAULT 'unknown',
		traffic_limit_bytes INTEGER, traffic_reset_day INTEGER, traffic_warn_percent INTEGER, traffic_limit_action TEXT, traffic_multiplier REAL,
		traffic_used INTEGER, traffic_period_start INTEGER, traffic_limited NUMERIC, traffic_alert_level INTEGER, traffic_disabled_inbounds TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`INSERT INTO nodes (name, address, port, api_token) VALUES ('legacy-node', '10.0.0.1', 2053, 'tok')`).Error; err != nil {
		t.Fatal(err)
	}
	if h, err := legacy.DB(); err == nil {
		_ = h.Close()
	}
	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB legacy schema: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	var nulls int64
	if err := GetDB().Table("nodes").Where("traffic_multiplier IS NULL OR traffic_limit_action IS NULL").Count(&nulls).Error; err != nil || nulls != 0 {
		t.Fatalf("rows with NULL quota columns after migration = %d, %v", nulls, err)
	}
	assertLegacyNodeTrafficDefaults(t)
}

func TestNodeTrafficLimitMigration_Postgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("XUI_DB_DSN"))
	if dsn == "" || os.Getenv("XUI_DB_TYPE") != "postgres" {
		t.Skip("set XUI_DB_TYPE=postgres and XUI_DB_DSN to run the node traffic limit migration test")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	schema := fmt.Sprintf("node_traffic_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = CloseDB()
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	scoped := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := u.Query()
		query.Set("search_path", schema)
		u.RawQuery = query.Encode()
		scoped = u.String()
	}
	t.Setenv("XUI_DB_DSN", scoped)
	legacy, err := gorm.Open(postgres.Open(scoped), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`CREATE TABLE nodes (id BIGSERIAL PRIMARY KEY, name TEXT, address TEXT, port BIGINT, api_token TEXT, enable BOOLEAN DEFAULT true, status TEXT DEFAULT 'unknown')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`INSERT INTO nodes (name, address, port, api_token) VALUES ('legacy-node', '10.0.0.1', 2053, 'tok')`).Error; err != nil {
		t.Fatal(err)
	}
	if h, err := legacy.DB(); err == nil {
		_ = h.Close()
	}
	if err := InitDB(""); err != nil {
		t.Fatalf("InitDB legacy schema: %v", err)
	}
	assertLegacyNodeTrafficDefaults(t)
}
