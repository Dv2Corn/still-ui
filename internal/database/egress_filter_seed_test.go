package database

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A new install starts with outgoing SMTP blocked; an install that already ran its
// seeders (an upgrade, or the operator turning the filter off) is never given one.
func TestInitDB_FreshInstallBlocksSMTPOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x-ui.db")
	read := func() (string, bool) {
		t.Helper()
		var s model.Setting
		err := db.Where("key = ?", "egressFilter").First(&s).Error
		return s.Value, err == nil
	}
	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	if got, ok := read(); !ok || got != FreshEgressFilter {
		t.Fatalf("fresh egressFilter = %q (present %v), want %q", got, ok, FreshEgressFilter)
	}
	if err := db.Where("key = ?", "egressFilter").Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := CloseDB(); err != nil {
		t.Fatal(err)
	}

	if err := InitDB(path); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	if got, ok := read(); ok {
		t.Fatalf("an existing install was given egressFilter %q", got)
	}
}
