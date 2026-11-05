package database

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestSubClashAutoGroupSeederKeepsExistingPanelsOff(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	open := func() {
		t.Helper()
		if err := InitDB(dbPath); err != nil {
			if strings.Contains(err.Error(), "CGO_ENABLED=0") {
				t.Skipf("sqlite needs cgo: %v", err)
			}
			t.Fatalf("InitDB: %v", err)
		}
	}
	reopen := func() {
		t.Helper()
		if err := CloseDB(); err != nil {
			t.Fatalf("CloseDB: %v", err)
		}
		open()
	}
	stored := func() []string {
		t.Helper()
		var values []string
		if err := db.Model(&model.Setting{}).Where("key = ?", "subClashAutoGroup").Pluck("value", &values).Error; err != nil {
			t.Fatalf("read subClashAutoGroup: %v", err)
		}
		return values
	}
	forgetSeeder := func() {
		t.Helper()
		if err := db.Where("seeder_name = ?", subClashAutoGroupSeeder).Delete(&model.HistoryOfSeeders{}).Error; err != nil {
			t.Fatalf("clear seeder history: %v", err)
		}
	}
	t.Cleanup(func() { _ = CloseDB() })

	open()
	if got := stored(); len(got) != 0 {
		t.Fatalf("fresh install stored subClashAutoGroup = %q, want no row (factory default on)", got)
	}
	var recorded int64
	if err := db.Model(&model.HistoryOfSeeders{}).Where("seeder_name = ?", subClashAutoGroupSeeder).Count(&recorded).Error; err != nil || recorded != 1 {
		t.Fatalf("fresh install seeder records = %d (err %v), want 1", recorded, err)
	}
	if err := db.Create(&model.User{Username: "admin", Password: "x"}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	reopen()
	if got := stored(); len(got) != 0 {
		t.Fatalf("restart of a fresh install stored %q, want no row", got)
	}

	// A panel upgraded from a release without the Auto group.
	forgetSeeder()
	reopen()
	if got := stored(); len(got) != 1 || got[0] != "false" {
		t.Fatalf("upgraded panel subClashAutoGroup = %q, want pinned false", got)
	}

	// An admin who already chose keeps the choice.
	if err := db.Model(&model.Setting{}).Where("key = ?", "subClashAutoGroup").Update("value", "true").Error; err != nil {
		t.Fatalf("save choice: %v", err)
	}
	forgetSeeder()
	reopen()
	if got := stored(); len(got) != 1 || got[0] != "true" {
		t.Fatalf("saved choice became %q, want true kept", got)
	}
}
