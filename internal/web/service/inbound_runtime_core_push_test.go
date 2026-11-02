package service

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// corePushRecorder keeps every handler operation and the last inbound handed
// to the local core, as the core would read it.
type corePushRecorder struct {
	fakeNodeRuntime
	mu    sync.Mutex
	ops   []string
	added *model.Inbound
}

func (r *corePushRecorder) AddInbound(_ context.Context, ib *model.Inbound) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, "add-inbound")
	r.added = ib
	return nil
}

func (r *corePushRecorder) DelInbound(context.Context, *model.Inbound) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, "del-inbound")
	return nil
}

func (r *corePushRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ops...)
}

// corePushVisionClient is one Vision client; email is unique per test, as a
// PostgreSQL test database is shared by the whole package.
func corePushVisionClient(email string) string {
	return `{"decryption":"none","clients":[{"email":"` + email + `","id":"5f2eb9d6-3a2f-4a55-9812-6ea1e2f7a401","flow":"xtls-rprx-vision","enable":true}]}`
}

const corePushReality = `"realitySettings":{"show":false,"target":"example.com:443","serverNames":["example.com"],"privateKey":"kL8zDqZRnUbzsg8W7a4lRVsFhkh3FBgJ0n7l7mX9Q1k","shortIds":["ab"]}`

func seedCorePushInbound(t *testing.T, tag string, port int, stream, settings string) *model.Inbound {
	t.Helper()
	seedInboundConflict(t, tag, "0.0.0.0", port, model.VLESS, stream, settings)
	stored := loadInboundByTag(t, tag)
	clients, err := (&InboundService{}).GetClients(stored)
	if err != nil {
		t.Fatalf("GetClients: %v", err)
	}
	if err := (&ClientService{}).SyncInbound(nil, stored.Id, clients); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}
	for _, c := range clients {
		mkTraffic(t, stored.Id, c.Email, 0, 0, 0, 0, true)
	}
	return stored
}

func corePushFlows(t *testing.T, ib *model.Inbound) map[string]string {
	t.Helper()
	cfg := ib.GenXrayInboundConfig()
	return clientFlowsInSettings(t, string(cfg.Settings))
}

// The stored client keeps Vision when its inbound moves to a stream Vision cannot
// run on; the generated config drops it there, and the core refuses a client
// that still carries it, so the pushed inbound must drop it too.
func TestUpdateInboundPushDropsFlowTheNewStreamCannotCarry(t *testing.T) {
	setupConflictDB(t)
	local := &corePushRecorder{}
	useTestRuntimeManager(t).SetLocalRuntimeOverride(local)

	stored := seedCorePushInbound(t, "flow-push", 50091,
		`{"network":"tcp","security":"tls","tlsSettings":{}}`, corePushVisionClient("flow-push-v"))

	edited := *stored
	edited.StreamSettings = `{"network":"ws","security":"tls","tlsSettings":{},"wsSettings":{"path":"/ws"}}`
	_, needRestart, err := (&InboundService{}).UpdateInbound(&edited)
	if err != nil {
		t.Fatalf("UpdateInbound: %v", err)
	}
	if needRestart || local.added == nil {
		t.Fatalf("a non-REALITY stream change must be hot-swapped; needRestart=%v ops=%q", needRestart, local.recorded())
	}
	if flow := corePushFlows(t, local.added)["flow-push-v"]; flow != "" {
		t.Fatalf("pushed client flow = %q, want none on a ws stream (the core rejects it)", flow)
	}
}

// Re-enabling an inbound whose stored clients carry a flow its stream cannot run
// pushes the same refused client.
func TestSetInboundEnablePushDropsFlowTheStreamCannotCarry(t *testing.T) {
	setupConflictDB(t)
	local := &corePushRecorder{}
	useTestRuntimeManager(t).SetLocalRuntimeOverride(local)

	stored := seedCorePushInbound(t, "flow-enable", 50092,
		`{"network":"ws","security":"none","wsSettings":{"path":"/ws"}}`, corePushVisionClient("flow-enable-v"))
	if err := database.GetDB().Model(stored).Update("enable", false).Error; err != nil {
		t.Fatalf("disable inbound: %v", err)
	}

	needRestart, err := (&InboundService{}).SetInboundEnable(stored.Id, true)
	if err != nil {
		t.Fatalf("SetInboundEnable: %v", err)
	}
	if needRestart || local.added == nil {
		t.Fatalf("enabling must hot-add the inbound; needRestart=%v ops=%q", needRestart, local.recorded())
	}
	if flow := corePushFlows(t, local.added)["flow-enable-v"]; flow != "" {
		t.Fatalf("pushed client flow = %q, want none on a ws stream", flow)
	}
}

// The generated config heals stream settings the core cannot run: a row stored
// before the save-time guard with REALITY plus a TCP finalmask crashes Xray on
// the first connection (XTLS/Xray-core#6453). Enabling pushed it unhealed.
func TestSetInboundEnablePushHealsTheStreamLikeTheGeneratedConfig(t *testing.T) {
	setupConflictDB(t)
	local := &corePushRecorder{}
	useTestRuntimeManager(t).SetLocalRuntimeOverride(local)

	stored := seedCorePushInbound(t, "heal-enable", 50093,
		`{"network":"tcp","security":"reality",`+corePushReality+`,"finalmask":{"tcp":[{"type":"fragment","settings":{"packets":"tlshello"}}]}}`,
		`{"decryption":"none","clients":[{"email":"heal-enable-h","id":"5f2eb9d6-3a2f-4a55-9812-6ea1e2f7a402","enable":true}]}`)
	if err := database.GetDB().Model(stored).Update("enable", false).Error; err != nil {
		t.Fatalf("disable inbound: %v", err)
	}

	if _, err := (&InboundService{}).SetInboundEnable(stored.Id, true); err != nil {
		t.Fatalf("SetInboundEnable: %v", err)
	}
	if local.added == nil {
		t.Fatalf("enabling must hot-add the inbound; ops=%q", local.recorded())
	}
	var stream map[string]any
	if err := json.Unmarshal(local.added.GenXrayInboundConfig().StreamSettings, &stream); err != nil {
		t.Fatalf("pushed stream: %v", err)
	}
	if _, ok := stream["finalmask"]; ok {
		t.Fatalf("pushed stream keeps a TCP finalmask on REALITY: %s", local.added.StreamSettings)
	}
}

// RestartXray restarts the core for a REALITY stream change because a gRPC
// remove+add does not reliably rebuild the REALITY authenticator; the inbound
// save pushed it over the API anyway and reported no restart.
func TestUpdateInboundRealityStreamChangeRestartsInsteadOfHotSwap(t *testing.T) {
	setupConflictDB(t)
	local := &corePushRecorder{}
	useTestRuntimeManager(t).SetLocalRuntimeOverride(local)

	stored := seedCorePushInbound(t, "reality-push", 50094,
		`{"network":"tcp","security":"reality",`+corePushReality+`}`, corePushVisionClient("reality-push-v"))

	edited := *stored
	edited.StreamSettings = `{"network":"xhttp","security":"reality",` + corePushReality + `,"xhttpSettings":{"path":"/x","mode":"auto"}}`
	_, needRestart, err := (&InboundService{}).UpdateInbound(&edited)
	if err != nil {
		t.Fatalf("UpdateInbound: %v", err)
	}
	if !needRestart {
		t.Fatal("needRestart = false, want a restart for a REALITY stream change")
	}
	if ops := local.recorded(); slices.Contains(ops, "add-inbound") {
		t.Fatalf("core operations = %q, want no hot add of a REALITY inbound the hot-diff refuses", ops)
	}
}

// A TPROXY inbound hot-added over gRPC reports success but binds no listener;
// RestartXray restarts for it, the create path hot-added it.
func TestAddInboundTproxyRestartsInsteadOfHotAdd(t *testing.T) {
	setupConflictDB(t)
	local := &corePushRecorder{}
	useTestRuntimeManager(t).SetLocalRuntimeOverride(local)

	_, needRestart, err := (&InboundService{}).AddInbound(&model.Inbound{
		UserId: 1, Enable: true, Port: 50095, Protocol: model.Tunnel,
		StreamSettings: `{"network":"tcp","security":"none","sockopt":{"tproxy":"tproxy"}}`,
		Settings:       `{"network":"tcp,udp","followRedirect":true}`,
	})
	if err != nil {
		t.Fatalf("AddInbound: %v", err)
	}
	if !needRestart {
		t.Fatal("needRestart = false, want a restart for a TPROXY inbound")
	}
	if ops := local.recorded(); slices.Contains(ops, "add-inbound") {
		t.Fatalf("core operations = %q, want no hot add of a TPROXY inbound", ops)
	}
}
