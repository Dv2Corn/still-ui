package job

import (
	"path/filepath"
	"testing"

	"github.com/op/go-logging"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	xuilogger "github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// The sync tick is the only place that sees which nodes it no longer fetches, so it
// must drop their online sets itself: a disabled node here, a deleted one below.
func TestNodeTrafficSyncDropsOnlineClientsOfUnsyncedNodes(t *testing.T) {
	xuilogger.InitLogger(logging.ERROR)
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }, SetNeedRestart: func() {}}))
	t.Cleanup(func() { runtime.SetManager(nil) })
	process := xray.NewTestProcess(nil, "")
	t.Cleanup(service.SetXrayProcessForTest(process))

	disabled := &model.Node{Name: "disabled", Address: "127.0.0.1", Port: 1, ApiToken: "tok", Enable: true, Status: "online"}
	if err := database.GetDB().Create(disabled).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := database.GetDB().Model(disabled).Update("enable", false).Error; err != nil {
		t.Fatalf("disable node: %v", err)
	}
	process.SetNodeOnlineTree(disabled.Id, map[string][]string{"g-disabled": {"a@x"}})
	process.SetNodeOnlineTree(disabled.Id+100, map[string][]string{"g-deleted": {"b@x"}})

	NewNodeTrafficSyncJob().Run()

	if got := process.GetMergedNodeTrees(); len(got) != 0 {
		t.Fatalf("online sets after a sync tick = %v, want none for a disabled or deleted node", got)
	}
}

// The tick's depletion pass removes a disabled client from the local core too; when
// that removal fails, only a restart takes the client out, so the flag must survive.
func TestNodeTrafficSyncKeepsNeedRestartOfAFailedLocalRemoval(t *testing.T) {
	xuilogger.InitLogger(logging.ERROR)
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	// API port 0: the local core cannot be reached, so its RemoveUser fails.
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }, SetNeedRestart: func() {}}))
	t.Cleanup(func() { runtime.SetManager(nil) })
	t.Cleanup(service.SetXrayProcessForTest(xray.NewTestProcess(nil, "")))
	if err := (&service.SettingService{}).SetRestartXrayOnClientDisable(false); err != nil {
		t.Fatalf("disable restart on client disable: %v", err)
	}

	db := database.GetDB()
	node := &model.Node{Name: "n1", Address: "127.0.0.1", Port: 1, ApiToken: "tok", Enable: true, Status: "online"}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	const email = "spent@x"
	ib := &model.Inbound{
		Tag: "local-vless", Enable: true, Protocol: model.VLESS, Port: 4330,
		Settings: `{"clients":[{"email":"spent@x","id":"11111111-1111-1111-1111-111111111111","enable":true}]}`,
	}
	if err := db.Create(ib).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	rec := &model.ClientRecord{Email: email, Enable: true}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: ib.Id}).Error; err != nil {
		t.Fatalf("link client: %v", err)
	}
	if err := db.Create(&xray.ClientTraffic{InboundId: ib.Id, Email: email, Enable: true, Up: 60, Down: 40, Total: 100}).Error; err != nil {
		t.Fatalf("create traffic: %v", err)
	}

	xraySvc := &service.XrayService{}
	xraySvc.IsNeedRestartAndSetFalse()
	NewNodeTrafficSyncJob().Run()

	var after xray.ClientTraffic
	if err := db.Where("email = ?", email).First(&after).Error; err != nil {
		t.Fatalf("read traffic: %v", err)
	}
	if after.Enable {
		t.Fatalf("the tick did not disable the depleted client")
	}
	if !xraySvc.IsNeedRestartAndSetFalse() {
		t.Fatalf("the failed local removal was dropped: no restart is pending, the client keeps being served")
	}
}
