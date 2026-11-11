package database

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// An upgraded panel keeps how it treated apps without a device id and keeps every registered
// device (no expiry); a value the admin already saved is not overwritten.
func TestHwidBehaviourKeptOnUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name       string
		limit      int
		stored     map[string]string
		wantPolicy string
		wantDays   string
	}{
		{name: "panel with a device limit", limit: 2, wantPolicy: HwidNoIdRefuse, wantDays: "0"},
		{name: "panel without device limits", limit: 0, wantPolicy: HwidNoIdAllow, wantDays: "0"},
		{
			name: "panel with saved choices", limit: 2,
			stored:     map[string]string{HwidNoIdPolicySetting: "shared", HwidExpireDaysSetting: "14"},
			wantPolicy: "shared", wantDays: "14",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "x-ui.db")
			if err := InitDB(dbPath); err != nil {
				t.Fatalf("InitDB: %v", err)
			}
			t.Cleanup(func() { _ = CloseDB() })
			if err := db.Create(&model.ClientRecord{Email: "a@x", SubID: "sub-a", Enable: true, LimitHwid: tc.limit}).Error; err != nil {
				t.Fatalf("seed client: %v", err)
			}
			for key, value := range tc.stored {
				if err := db.Create(&model.Setting{Key: key, Value: value}).Error; err != nil {
					t.Fatalf("seed %s: %v", key, err)
				}
			}
			if err := db.Where("seeder_name = ?", "HwidUpgradeKeep").Delete(&model.HistoryOfSeeders{}).Error; err != nil {
				t.Fatalf("clear seeder history: %v", err)
			}
			if err := CloseDB(); err != nil {
				t.Fatalf("CloseDB: %v", err)
			}
			if err := InitDB(dbPath); err != nil {
				t.Fatalf("reopen InitDB: %v", err)
			}

			for key, want := range map[string]string{HwidNoIdPolicySetting: tc.wantPolicy, HwidExpireDaysSetting: tc.wantDays} {
				var stored []string
				if err := db.Model(&model.Setting{}).Where("key = ?", key).Pluck("value", &stored).Error; err != nil {
					t.Fatalf("read %s: %v", key, err)
				}
				if len(stored) != 1 || stored[0] != want {
					t.Fatalf("stored %s = %q, want exactly [%s]", key, stored, want)
				}
			}
		})
	}
}

// A new panel has no stored choice, so it takes the defaults of this version, and still does
// after a restart: the seeder must not mistake its second start for an upgrade.
func TestHwidUpgradeKeepSkipsNewPanel(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	if err := CloseDB(); err != nil {
		t.Fatalf("CloseDB: %v", err)
	}
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("restart InitDB: %v", err)
	}
	var stored int64
	if err := db.Model(&model.Setting{}).Where("key IN ?", []string{HwidNoIdPolicySetting, HwidExpireDaysSetting}).Count(&stored).Error; err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if stored != 0 {
		t.Fatalf("a new panel got %d pinned device-limit settings, want none", stored)
	}
}
