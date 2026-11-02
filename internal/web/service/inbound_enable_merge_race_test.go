package service

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"

	"gorm.io/gorm"
)

// A node merge samples config_dirty and later writes the node's enable state onto the
// central row. A toggle committed in between was overwritten by the stale snapshot and,
// with the node then marked dirty, pushed back to the node: the switch did nothing.
// The toggle must queue behind the merge on the serialized writer.
func TestSetInboundEnableIsNotOverwrittenByAConcurrentNodeMerge(t *testing.T) {
	db := initTrafficTestDB(t)
	node := &model.Node{Name: "n1", Address: "127.0.0.1", Port: 1, ApiToken: "tok", Enable: true, Status: "offline"}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	createNodeInbound(t, db, node.Id, "n1-in", 41001)
	var ib model.Inbound
	if err := db.Where("tag = ?", "n1-in").First(&ib).Error; err != nil {
		t.Fatalf("read inbound: %v", err)
	}

	resetTrafficWriterForTest(t)
	StartTrafficWriter()
	svc := &InboundService{}

	// Toggle the inbound off right after the merge has read the node's dirty flag.
	toggled := make(chan error, 1)
	var once sync.Once
	const hook = "test:toggle_after_dirty_read"
	if err := db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "nodes" || !strings.Contains(tx.Statement.SQL.String(), "config_dirty") {
			return
		}
		once.Do(func() {
			go func() {
				_, err := svc.SetInboundEnable(ib.Id, false)
				toggled <- err
			}()
			// Long enough for an unserialized toggle to commit; a queued one waits.
			select {
			case err := <-toggled:
				toggled <- err
			case <-time.After(300 * time.Millisecond):
			}
		})
	}); err != nil {
		t.Fatalf("register hook: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(hook) })

	snap := &runtime.TrafficSnapshot{Inbounds: []*model.Inbound{{
		Tag: "n1-in", Enable: true, Port: 41001, Protocol: model.VLESS, Settings: `{"clients":[]}`,
	}}}
	if _, err := svc.SetRemoteTraffic(node.Id, snap, false, false); err != nil {
		t.Fatalf("SetRemoteTraffic: %v", err)
	}
	select {
	case err := <-toggled:
		if err != nil {
			t.Fatalf("SetInboundEnable: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SetInboundEnable did not finish")
	}

	var after model.Inbound
	if err := db.Where("id = ?", ib.Id).First(&after).Error; err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if after.Enable {
		t.Fatalf("the operator's disable was overwritten by the node snapshot merged concurrently")
	}
}
