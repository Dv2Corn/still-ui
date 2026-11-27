package runtime

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// recordingCore is a fake Xray handler service that records user operations.
type recordingCore struct {
	okHandlerServer
	mu      sync.Mutex
	ops     []string          // "remove:<email>" or "add:<email>"
	failFor map[string]string // email -> error message for a remove
	users   []string          // emails GetInboundUsers reports
}

func (c *recordingCore) AlterInbound(_ context.Context, req *command.AlterInboundRequest) (*command.AlterInboundResponse, error) {
	inst, err := req.GetOperation().GetInstance()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch op := inst.(type) {
	case *command.RemoveUserOperation:
		if msg, ok := c.failFor[op.Email]; ok {
			return nil, status.Error(codes.Unknown, msg)
		}
		c.ops = append(c.ops, "remove:"+op.Email)
	case *command.AddUserOperation:
		c.ops = append(c.ops, "add:"+op.GetUser().GetEmail())
	}
	return &command.AlterInboundResponse{}, nil
}

func (c *recordingCore) GetInboundUsers(context.Context, *command.GetInboundUserRequest) (*command.GetInboundUserResponse, error) {
	resp := &command.GetInboundUserResponse{}
	for _, e := range c.users {
		resp.Users = append(resp.Users, &protocol.User{Email: e})
	}
	return resp, nil
}

func (c *recordingCore) take() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := strings.Join(c.ops, ",")
	c.ops = nil
	return s
}

func startRecordingCore(t *testing.T, c *recordingCore) *Local {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	command.RegisterHandlerServiceServer(server, c)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	port := listener.Addr().(*net.TCPAddr).Port
	return NewLocal(LocalDeps{APIPort: func() int { return port }})
}

func TestLocalRemoveUserRoutesByProtocol(t *testing.T) {
	ctx := context.Background()
	core := &recordingCore{users: []string{"other"}}
	local := startRecordingCore(t, core)

	if err := local.RemoveUser(ctx, &model.Inbound{Tag: "v", Protocol: model.VLESS}, "a"); err != nil || core.take() != "remove:a" {
		t.Fatalf("vless: err=%v", err)
	}
	// WireGuard removes only peers the core actually has under that email.
	if err := local.RemoveUser(ctx, &model.Inbound{Tag: "w", Protocol: model.WireGuard}, "a"); err != nil || core.take() != "" {
		t.Fatalf("wireguard without the peer: err=%v", err)
	}
	core.users = []string{"a"}
	if err := local.RemoveUser(ctx, &model.Inbound{Tag: "w", Protocol: model.WireGuard}, "a"); err != nil || core.take() != "remove:a" {
		t.Fatalf("wireguard with the peer: err=%v", err)
	}
	// Sidecar protocols never touch Xray, even when it is down.
	stopped := NewLocal(LocalDeps{APIPort: func() int { return 0 }})
	for _, p := range []model.Protocol{model.MTProto, model.AmneziaWG} {
		if err := stopped.RemoveUser(ctx, &model.Inbound{Protocol: p}, "a"); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestLocalDeleteUser(t *testing.T) {
	ctx := context.Background()
	core := &recordingCore{failFor: map[string]string{"gone": "user gone not found", "busy": "core busy"}}
	local := startRecordingCore(t, core)
	ib := &model.Inbound{Tag: "v", Protocol: model.VLESS}
	if err := local.DeleteUser(ctx, ib, ""); err != nil || core.take() != "" {
		t.Fatalf("empty email: err=%v", err)
	}
	if err := local.DeleteUser(ctx, ib, "a"); err != nil || core.take() != "remove:a" {
		t.Fatalf("delete: err=%v", err)
	}
	if err := local.DeleteUser(ctx, ib, "gone"); err != nil {
		t.Fatalf("a user already gone is not an error: %v", err)
	}
	if err := local.DeleteUser(ctx, ib, "busy"); err == nil {
		t.Fatal("other core errors must be returned")
	}
}

func TestLocalUpdateUser(t *testing.T) {
	ctx := context.Background()
	core := &recordingCore{failFor: map[string]string{"gone": "user gone not found", "busy": "core busy"}}
	local := startRecordingCore(t, core)
	ib := &model.Inbound{Tag: "v", Protocol: model.VLESS}
	on := model.Client{Email: "new", Enable: true, ID: "11111111-1111-1111-1111-111111111111"}

	if err := local.UpdateUser(ctx, ib, "", on); err != nil || core.take() != "add:new" {
		t.Fatalf("no old email: err=%v", err)
	}
	if err := local.UpdateUser(ctx, ib, "old", on); err != nil || core.take() != "remove:old,add:new" {
		t.Fatalf("rename: err=%v", err)
	}
	if err := local.UpdateUser(ctx, ib, "gone", on); err != nil || core.take() != "add:new" {
		t.Fatalf("old already gone: err=%v", err)
	}
	if err := local.UpdateUser(ctx, ib, "busy", on); err == nil || core.take() != "" {
		t.Fatalf("a failing remove must stop the update: err=%v", err)
	}
	if err := local.UpdateUser(ctx, ib, "old", model.Client{Email: "new"}); err != nil || core.take() != "remove:old" {
		t.Fatalf("disabled client: err=%v", err)
	}
}

func TestLocalSidecarCallbacks(t *testing.T) {
	ctx := context.Background()
	restarts, reconciles := 0, 0
	local := NewLocal(LocalDeps{
		APIPort:          func() int { return 0 },
		SetNeedRestart:   func() { restarts++ },
		RequestReconcile: func() { reconciles++ },
	})
	if err := local.DelInbound(ctx, &model.Inbound{Id: 987001, Protocol: model.AmneziaWG}); err != nil || restarts != 1 {
		t.Fatalf("awg delete: err=%v restarts=%d", err, restarts)
	}
	if err := local.DelInbound(ctx, &model.Inbound{Id: 987002, Protocol: model.TUIC}); err != nil || reconciles != 1 {
		t.Fatalf("tuic delete: err=%v reconciles=%d", err, reconciles)
	}
	if err := local.AddInbound(ctx, &model.Inbound{Id: 987003, Protocol: model.TUIC, Settings: "{}"}); err != nil || reconciles != 2 {
		t.Fatalf("tuic add: err=%v reconciles=%d", err, reconciles)
	}
	if err := local.RestartXray(ctx); err != nil || restarts != 2 {
		t.Fatalf("RestartXray: err=%v restarts=%d", err, restarts)
	}

	// Every callback is optional.
	bare := NewLocal(LocalDeps{APIPort: func() int { return 0 }})
	for _, ib := range []*model.Inbound{{Id: 987004, Protocol: model.AmneziaWG}, {Id: 987005, Protocol: model.TUIC}} {
		if err := bare.DelInbound(ctx, ib); err != nil {
			t.Fatal(err)
		}
	}
	if err := bare.AddInbound(ctx, &model.Inbound{Id: 987006, Protocol: model.TUIC, Settings: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := bare.RestartXray(ctx); err != nil {
		t.Fatal(err)
	}
	if err := bare.UpdateInbound(ctx, &model.Inbound{Id: 987007, Protocol: model.AmneziaWG}, &model.Inbound{Id: 987007, Protocol: model.AmneziaWG}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveAmneziaWGWarnsOnlyOnFailure(t *testing.T) {
	removeAmneziaWG(987654)
	for _, line := range logger.GetLogs(1000, "DEBUG") {
		if strings.Contains(line, "removing inbound 987654") {
			t.Fatalf("a clean removal logged a warning: %q", line)
		}
	}
}

// Moving an inbound off or onto a sidecar protocol runs the Xray side exactly once.
func TestLocalUpdateAcrossSidecarProtocols(t *testing.T) {
	ctx := context.Background()
	port := startOKCore(t)
	vless := func(enable bool) *model.Inbound {
		return &model.Inbound{
			Id: 9, Tag: "x9", Protocol: model.VLESS, Port: 40309, Enable: enable,
			Settings: `{"clients":[],"decryption":"none"}`, StreamSettings: "{}", Sniffing: "{}",
		}
	}
	for _, side := range []model.Protocol{model.MTProto, model.AmneziaWG} {
		applied, removed, restarts := 0, "", 0
		local := NewLocal(LocalDeps{
			APIPort:        func() int { return port },
			SetNeedRestart: func() { restarts++ },
			InboundApplied: func(*xray.InboundConfig) { applied++ },
			InboundRemoved: func(tag string) { removed = tag },
		})
		sidecar := &model.Inbound{Id: 9, Tag: "x9", Protocol: side, Enable: true, Settings: "{}"}

		if err := local.UpdateInbound(ctx, sidecar, vless(true)); err != nil || applied != 1 || removed != "" {
			t.Fatalf("%s -> vless: err=%v applied=%d removed=%q", side, err, applied, removed)
		}
		if err := local.UpdateInbound(ctx, sidecar, vless(false)); err != nil || applied != 1 {
			t.Fatalf("%s -> disabled vless: err=%v applied=%d", side, err, applied)
		}
		if err := local.UpdateInbound(ctx, vless(true), sidecar); err != nil || removed != "x9" || applied != 1 {
			t.Fatalf("vless -> %s: err=%v removed=%q applied=%d", side, err, removed, applied)
		}
		removed = ""
		if err := local.UpdateInbound(ctx, sidecar, sidecar); err != nil || removed != "" || applied != 1 {
			t.Fatalf("%s -> %s: err=%v removed=%q applied=%d", side, side, err, removed, applied)
		}
		if side == model.AmneziaWG && restarts != 4 {
			t.Fatalf("awg updates flagged %d restarts, want one per update (4)", restarts)
		}
	}
}
