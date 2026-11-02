package service

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

// New devices of one subscription arriving at once must not all pass the count
// before any of them is inserted: limit_hwid=1 admits exactly one.
func TestEnforceHwidParallelNewDevicesKeepLimit(t *testing.T) {
	initClientHwidTestDB(t)
	if config.GetDBKind() != "postgres" {
		t.Skip("set XUI_DB_TYPE=postgres and XUI_DB_DSN: SQLite runs one writer at a time")
	}
	db := database.GetDB()
	suffix := time.Now().UnixNano()
	rec := &model.ClientRecord{
		Email:     fmt.Sprintf("hwid-race-%d@x", suffix),
		SubID:     fmt.Sprintf("sub-hwid-race-%d", suffix),
		UUID:      "11111111-2222-4333-8444-5555555555a1",
		Enable:    true,
		LimitHwid: 1,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("seed client: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Where("sub_id = ?", rec.SubID).Delete(&model.ClientHwid{}).Error
		_ = db.Delete(&model.ClientRecord{}, rec.Id).Error
	})

	// Widen the window between the gate's device count and its insert.
	var widen atomic.Bool
	widen.Store(true)
	if err := db.Callback().Query().After("gorm:query").Register("t:hwid:widen", func(tx *gorm.DB) {
		if widen.Load() && tx.Statement != nil && tx.Statement.Table == "client_hwids" {
			time.Sleep(30 * time.Millisecond)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("t:hwid:widen") })

	const devices = 12
	var wg sync.WaitGroup
	var allowed atomic.Int32
	errs := make(chan error, devices)
	for i := range devices {
		wg.Go(func() {
			res, err := (&ClientService{}).EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: fmt.Sprintf("device-%02d-%d", i, suffix)})
			if err != nil {
				errs <- err
				return
			}
			if res.Allowed {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	widen.Store(false)
	close(errs)
	for err := range errs {
		t.Fatalf("EnforceHwidForSubID: %v", err)
	}
	var registered int64
	if err := db.Model(&model.ClientHwid{}).Where("sub_id = ?", rec.SubID).Count(&registered).Error; err != nil {
		t.Fatalf("count devices: %v", err)
	}
	if registered != 1 || allowed.Load() != 1 {
		t.Fatalf("registered=%d allowed=%d of %d parallel new devices, want 1 and 1 with limit_hwid=1", registered, allowed.Load(), devices)
	}
}
