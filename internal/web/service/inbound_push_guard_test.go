package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func pushGuardTestService(t *testing.T) *InboundService {
	t.Helper()
	setupConflictDB(t)
	mgr := useTestRuntimeManager(t)
	mgr.SetLocalRuntimeOverride(&fakeNodeRuntime{})
	return &InboundService{FromNodeSync: true}
}

// 127.0.0.1.nip.io resolves to loopback when the core dials it, so a name passes the
// lexical check; on this node's own ports the guard must refuse it anyway.
func TestPushedRelayTargetNameOnThisNodesOwnPortIsRefused(t *testing.T) {
	push := pushGuardTestService(t)
	for i, tc := range []struct{ settings, stream, want string }{
		{
			`{"clients":[],"fallbacks":[{"dest":"127.0.0.1.nip.io:62789"}]}`, `{"network":"tcp"}`,
			`may not set fallback dest to "127.0.0.1.nip.io:62789"`,
		},
		{
			`{"clients":[]}`, `{"network":"tcp","security":"reality","realitySettings":{"target":"127.0.0.1.nip.io:2053"}}`,
			`may not set REALITY target to "127.0.0.1.nip.io:2053"`,
		},
		{
			`{"clients":[]}`, `{"network":"tcp","security":"reality","realitySettings":{"dest":"sub.example.com:2096"}}`,
			`may not set REALITY target to "sub.example.com:2096"`,
		},
		// Go's dialer reads a leading zero as the same port.
		{
			`{"clients":[],"fallbacks":[{"dest":"127.0.0.1.nip.io:062789"}]}`, `{"network":"tcp"}`,
			`may not set fallback dest to "127.0.0.1.nip.io:062789"`,
		},
	} {
		ib := &model.Inbound{Enable: true, Port: 44420 + i, Protocol: model.VLESS, Settings: tc.settings, StreamSettings: tc.stream}
		if _, _, err := push.AddInbound(ib); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("AddInbound = %v, want %q", err, tc.want)
		}
	}
	public := &model.Inbound{Enable: true, Port: 44430, Protocol: model.VLESS, Settings: `{"clients":[]}`,
		StreamSettings: `{"network":"tcp","security":"reality","realitySettings":{"target":"www.example.com:443"}}`}
	if _, _, err := push.AddInbound(public); err != nil && strings.Contains(err.Error(), "may not set") {
		t.Errorf("public REALITY target refused: %v", err)
	}
}
