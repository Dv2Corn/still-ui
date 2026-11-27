package database

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Boundary tests written against surviving mutants reported by gremlins (nightly mutation run).

func TestWireguardPeerEmailNumbering(t *testing.T) {
	if got := wireguardPeerEmail("", map[string]any{}, 0, map[string]struct{}{}); got != "wg-1" {
		t.Fatalf("first peer = %q, want wg-1", got)
	}
	used := map[string]struct{}{"my-wg-1": {}, "my-wg-1-2": {}}
	if got := wireguardPeerEmail("my wg", map[string]any{}, 0, used); got != "my-wg-1-3" {
		t.Fatalf("taken names: got %q, want my-wg-1-3", got)
	}
}

func TestMtprotoInboundClientEmailNumbering(t *testing.T) {
	if got := mtprotoInboundClientEmail("  ", map[string]struct{}{}); got != "mtproto" {
		t.Fatalf("empty remark = %q, want mtproto", got)
	}
	used := map[string]struct{}{"tg-proxy": {}, "tg-proxy-2": {}}
	if got := mtprotoInboundClientEmail("tg proxy", used); got != "tg-proxy-3" {
		t.Fatalf("taken names: got %q, want tg-proxy-3", got)
	}
}

func TestLegacyDNSBlockTypesBounds(t *testing.T) {
	got := legacyDNSBlockTypes([]any{"0,65535", "-1", "65536", 28.0})
	if want := []int{0, 65535, 28}; !reflect.DeepEqual(got, want) {
		t.Fatalf("legacyDNSBlockTypes = %v, want %v", got, want)
	}
}

func TestEnvIntReadsAPositiveValue(t *testing.T) {
	t.Setenv("XUI_TEST_ENV_INT", "5")
	if got := envInt("XUI_TEST_ENV_INT", 32); got != 5 {
		t.Fatalf("envInt = %d, want 5", got)
	}
	t.Setenv("XUI_TEST_ENV_INT", "")
	if got := envInt("XUI_TEST_ENV_INT", 32); got != 32 {
		t.Fatalf("envInt with empty value = %d, want default 32", got)
	}
}

// Every upgrade step must run and leave its history row: a step that returns early
// on success would silently skip all the steps after it.
func TestUpgradeRunsEverySeeder(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	var fresh []string
	if err := db.Model(&model.HistoryOfSeeders{}).Pluck("seeder_name", &fresh).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&model.HistoryOfSeeders{}).Error; err != nil {
		t.Fatalf("reset seeder history: %v", err)
	}
	// normalizeSettingPaths runs last on every start: a step that returned early would skip it.
	db.Where("key = ?", "subPath").Delete(&model.Setting{})
	if err := db.Create(&model.Setting{Key: "subPath", Value: "/sub"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := runSeeders(false); err != nil {
		t.Fatalf("runSeeders: %v", err)
	}
	var subPath model.Setting
	db.Where("key = ?", "subPath").First(&subPath)
	if subPath.Value != "/sub/" {
		t.Errorf("subPath = %q after upgrade, want /sub/ (normalizeSettingPaths did not run)", subPath.Value)
	}
	var after []string
	if err := db.Model(&model.HistoryOfSeeders{}).Pluck("seeder_name", &after).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range fresh {
		if !slices.Contains(after, name) {
			t.Errorf("upgrade did not record seeder %q", name)
		}
	}
}
