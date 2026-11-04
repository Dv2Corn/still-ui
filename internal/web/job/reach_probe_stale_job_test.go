package job

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The minute job also trims the probe history, but at most once an hour.
func TestReachProbeJobPrunesHistoryHourly(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	dbtest.InitDB(t, filepath.Join(dbDir, "x-ui.db"))
	db := database.GetDB()
	addOld := func() int {
		r := model.ReachProbeResult{ProbeId: 1, TargetKey: "gone", At: time.Now().Add(-8 * 24 * time.Hour).UnixMilli()}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		return r.Id
	}
	exists := func(id int) bool {
		var n int64
		db.Model(&model.ReachProbeResult{}).Where("id = ?", id).Count(&n)
		return n > 0
	}

	start := time.Now()
	clock := start
	j := NewReachProbeStaleJob()
	j.now = func() time.Time { return clock }

	first := addOld()
	j.Run()
	if exists(first) {
		t.Fatal("the first run did not prune results older than the history")
	}
	second := addOld()
	clock = start.Add(10 * time.Minute)
	j.Run()
	if !exists(second) {
		t.Fatal("pruned again within the hour")
	}
	clock = start.Add(61 * time.Minute)
	j.Run()
	if exists(second) {
		t.Fatal("not pruned after an hour")
	}
}
