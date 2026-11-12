package database

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestSubLeakAlertSeederKeepsExistingPanelsOff(t *testing.T) {
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
		if err := db.Model(&model.Setting{}).Where("key = ?", "tgEnabledEvents").Pluck("value", &values).Error; err != nil {
			t.Fatalf("read tgEnabledEvents: %v", err)
		}
		return values
	}
	forgetSeeder := func() {
		t.Helper()
		if err := db.Where("seeder_name = ?", subLeakAlertSeeder).Delete(&model.HistoryOfSeeders{}).Error; err != nil {
			t.Fatalf("clear seeder history: %v", err)
		}
	}
	t.Cleanup(func() { _ = CloseDB() })

	open()
	if got := stored(); len(got) != 0 {
		t.Fatalf("fresh install stored tgEnabledEvents = %q, want no row (factory default with sub.leak)", got)
	}
	if err := db.Create(&model.User{Username: "admin", Password: "x"}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	reopen()
	if got := stored(); len(got) != 0 {
		t.Fatalf("restart of a fresh install stored %q, want no row", got)
	}

	// A panel upgraded from a release without the alert, whose admin never saved the events.
	forgetSeeder()
	reopen()
	if got := stored(); len(got) != 1 || got[0] != TgEnabledEventsBeforeSubLeak || strings.Contains(got[0], "sub.leak") {
		t.Fatalf("upgraded panel tgEnabledEvents = %q, want the events it had, without sub.leak", got)
	}

	// An admin who already chose keeps the choice.
	if err := db.Model(&model.Setting{}).Where("key = ?", "tgEnabledEvents").Update("value", "login.attempt").Error; err != nil {
		t.Fatalf("save choice: %v", err)
	}
	forgetSeeder()
	reopen()
	if got := stored(); len(got) != 1 || got[0] != "login.attempt" {
		t.Fatalf("saved choice became %q, want login.attempt kept", got)
	}
}
