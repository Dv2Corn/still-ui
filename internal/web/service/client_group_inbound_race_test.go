package service

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

// A traffic tick that commits while a group op is between reading an inbound and
// writing it back must survive: the op only owns the clients' group field.
func TestGroupOpsKeepConcurrentInboundCounters(t *testing.T) {
	setupConflictDB(t)
	if config.GetDBKind() != "postgres" {
		t.Skip("set XUI_DB_TYPE=postgres and XUI_DB_DSN: SQLite runs one writer at a time")
	}
	db := database.GetDB()
	suffix := time.Now().UnixNano()
	email := fmt.Sprintf("grp-race-%d@x", suffix)
	inbound := durableTestInbound(nil, fmt.Sprintf("grp-race-%d", suffix), 20000+int(suffix%20000))
	inbound.Settings = `{"clients":[{"id":"8a9c1b2e-1111-4c3d-9e8f-000000000c01","email":"` + email + `","enable":true}],"decryption":"none"}`
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	groups := []string{fmt.Sprintf("vip-%d", suffix), fmt.Sprintf("gold-%d", suffix)}
	t.Cleanup(func() {
		_ = db.Where("email = ?", email).Delete(&model.ClientRecord{}).Error
		_ = db.Where("name IN ?", groups).Delete(&model.ClientGroup{}).Error
		_ = db.Delete(&model.Inbound{}, inbound.Id).Error
	})
	clients, err := (&InboundService{}).GetClients(inbound)
	if err != nil {
		t.Fatalf("parse clients: %v", err)
	}
	if err := (&ClientService{}).SyncInbound(db, inbound.Id, clients); err != nil {
		t.Fatalf("link client: %v", err)
	}

	// The tick lands on its own connection right after the op first reads the inbound.
	var armed atomic.Bool
	if err := db.Callback().Query().After("gorm:query").Register("t:group:tick", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "inbounds" || !armed.CompareAndSwap(true, false) {
			return
		}
		if err := db.Exec("UPDATE inbounds SET up = up + 777, down = down + 888 WHERE id = ?", inbound.Id).Error; err != nil {
			t.Errorf("concurrent tick: %v", err)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("t:group:tick") })

	svc := &ClientService{}
	steps := []struct {
		name      string
		run       func() (int, error)
		wantGroup string
	}{
		{"add", func() (int, error) { return svc.AddToGroup([]string{email}, groups[0]) }, groups[0]},
		{"rename", func() (int, error) { return svc.RenameGroup(groups[0], groups[1]) }, groups[1]},
		{"delete", func() (int, error) { return svc.DeleteGroup(groups[1]) }, ""},
	}
	for i, step := range steps {
		armed.Store(true)
		if n, err := step.run(); err != nil || n != 1 {
			t.Fatalf("%s: affected=%d err=%v, want 1 nil", step.name, n, err)
		}
		if armed.Load() {
			t.Fatalf("%s: the op never read the inbound row", step.name)
		}
		var got model.Inbound
		if err := db.First(&got, inbound.Id).Error; err != nil {
			t.Fatalf("%s: reload inbound: %v", step.name, err)
		}
		ticks := int64(i + 1)
		if got.Up != 777*ticks || got.Down != 888*ticks {
			t.Fatalf("%s: up/down = %d/%d, want %d/%d — the op wrote back counters it read before the tick", step.name, got.Up, got.Down, 777*ticks, 888*ticks)
		}
		hasGroup := strings.Contains(got.Settings, `"group":"`+step.wantGroup+`"`) || strings.Contains(got.Settings, `"group": "`+step.wantGroup+`"`)
		if step.wantGroup != "" && !hasGroup {
			t.Fatalf("%s: settings lost the group: %s", step.name, got.Settings)
		}
		if step.wantGroup == "" && strings.Contains(got.Settings, `"group"`) {
			t.Fatalf("%s: settings still carry a group: %s", step.name, got.Settings)
		}
	}
}
