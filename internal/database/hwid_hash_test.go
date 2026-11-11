package database

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func reopenWithoutSeeder(t *testing.T, dbPath, seeder string) {
	t.Helper()
	if err := db.Where("seeder_name = ?", seeder).Delete(&model.HistoryOfSeeders{}).Error; err != nil {
		t.Fatalf("clear seeder history: %v", err)
	}
	if err := CloseDB(); err != nil {
		t.Fatalf("CloseDB: %v", err)
	}
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("reopen InitDB: %v", err)
	}
}

// Rows written before the hash was keyed hold a plain SHA-256. The upgrade must re-key them
// in place, exactly once, or every registered device would count as a new one.
func TestClientHwidRowsRekeyedOnUpgrade(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	const legacy = "5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8"
	for _, sub := range []string{"sub-a", "sub-b"} {
		if err := db.Create(&model.ClientRecord{Email: sub + "@x", SubID: sub, Enable: true}).Error; err != nil {
			t.Fatalf("seed client %s: %v", sub, err)
		}
		if err := db.Create(&model.ClientHwid{SubID: sub, HwidHash: legacy, FirstSeen: 1, LastSeen: 2}).Error; err != nil {
			t.Fatalf("seed hwid %s: %v", sub, err)
		}
	}
	reopenWithoutSeeder(t, dbPath, "ClientHwidKeyedHash")

	key, err := HwidHashKey(db)
	if err != nil {
		t.Fatalf("HwidHashKey: %v", err)
	}
	readHashes := func() map[string]string {
		t.Helper()
		var rows []model.ClientHwid
		if err := db.Find(&rows).Error; err != nil {
			t.Fatalf("read hwids: %v", err)
		}
		got := map[string]string{}
		for _, row := range rows {
			got[row.SubID] = row.HwidHash
		}
		return got
	}
	got := readHashes()
	for _, sub := range []string{"sub-a", "sub-b"} {
		if want := KeyedHwidHash(key, sub, legacy); got[sub] != want {
			t.Fatalf("%s hash after upgrade = %q, want the keyed hash %q", sub, got[sub], want)
		}
	}
	if got["sub-a"] == got["sub-b"] {
		t.Fatalf("one device on two subscriptions kept the same hash %q", got["sub-a"])
	}

	if err := CloseDB(); err != nil {
		t.Fatalf("CloseDB: %v", err)
	}
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("second reopen InitDB: %v", err)
	}
	if again := readHashes(); again["sub-a"] != got["sub-a"] || again["sub-b"] != got["sub-b"] {
		t.Fatalf("hashes re-keyed twice: %v, want %v", again, got)
	}
}

// A panel that already limited devices refused apps without a device id. The upgrade pins
// that; a panel without limits takes the new default.
func TestHwidNoIdRefusalKeptOnUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		want  []string
	}{
		{name: "panel with a device limit", limit: 2, want: []string{HwidNoIdRefuse}},
		{name: "panel without device limits", limit: 0, want: nil},
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
			reopenWithoutSeeder(t, dbPath, "HwidNoIdPolicyKeep")

			var stored []string
			if err := db.Model(&model.Setting{}).Where("key = ?", HwidNoIdPolicySetting).Pluck("value", &stored).Error; err != nil {
				t.Fatalf("read policy: %v", err)
			}
			if len(stored) != len(tc.want) || (len(stored) == 1 && stored[0] != tc.want[0]) {
				t.Fatalf("stored %s = %q, want %q", HwidNoIdPolicySetting, stored, tc.want)
			}
		})
	}
}

func TestHwidHashKeyIsStable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	first, err := HwidHashKey(db)
	if err != nil || len(first) != 64 {
		t.Fatalf("key = %q (len %d), err %v; want 64 hex characters", first, len(first), err)
	}
	if err := CloseDB(); err != nil {
		t.Fatalf("CloseDB: %v", err)
	}
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("reopen InitDB: %v", err)
	}
	second, err := HwidHashKey(db)
	if err != nil || string(second) != string(first) {
		t.Fatalf("key after reopen = %q, err %v; want the same %q", second, err, first)
	}
}
