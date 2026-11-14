package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
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

// A local fallback dest passes only as the address an enabled inbound here listens on: a
// row the master pushed itself (disabled, or on a foreign listen) must not open any host.
func TestPushedFallbackDestMustBeAnEnabledLocalInboundsAddress(t *testing.T) {
	push := pushGuardTestService(t)
	if _, _, err := push.AddInbound(&model.Inbound{Enable: false, Port: 45432, Protocol: model.VLESS, Settings: `{"clients":[]}`}); err != nil {
		t.Fatalf("push a disabled decoy: %v", err)
	}
	if _, _, err := push.AddInbound(&model.Inbound{Enable: true, Listen: "10.0.0.5", Port: 45435, Protocol: model.VLESS, Settings: `{"clients":[]}`}); err != nil {
		t.Fatalf("push a decoy listening on a foreign address: %v", err)
	}
	seedInboundConflict(t, "named", "metadata", 45436, model.VLESS, `{"network":"tcp"}`, `{"clients":[]}`)
	seedInboundConflict(t, "wildcard", "", 45433, model.VLESS, `{"network":"tcp"}`, `{"clients":[]}`)
	seedInboundConflict(t, "loopback", "127.0.0.1", 45434, model.VLESS, `{"network":"tcp"}`, `{"clients":[]}`)
	seedInboundConflict(t, "socket", "/dev/shm/in.sock,0666", 0, model.VLESS, `{"network":"tcp"}`, `{"clients":[]}`)
	seedInboundConflict(t, "on-the-panel-port", "", 2053, model.VLESS, `{"network":"tcp"}`, `{"clients":[]}`)

	port := 44440
	fallback := func(dest string) error {
		port++
		_, _, err := push.AddInbound(&model.Inbound{Enable: true, Port: port, Protocol: model.VLESS,
			Settings: `{"clients":[],"fallbacks":[{"dest":` + dest + `}]}`, StreamSettings: `{"network":"tcp"}`})
		return err
	}
	for _, dest := range []string{
		`"169.254.169.254:45432"`, `"127.0.0.1:45432"`, `45432`, `"10.0.0.5:45433"`, `"192.168.1.1:45434"`,
		`"10.0.0.5:45435"`, `"metadata:45436"`, `"127.0.0.1:2053"`, `"127.0.0.1:045433"`,
	} {
		if err := fallback(dest); err == nil || !strings.Contains(err.Error(), "may not set fallback dest") {
			t.Errorf("fallback dest %s = %v, want refused by the push guard", dest, err)
		}
	}
	for _, dest := range []string{`45433`, `"127.0.0.1:45433"`, `"127.0.0.1:45434"`, `"/dev/shm/in.sock"`} {
		if err := fallback(dest); err != nil {
			t.Errorf("fallback dest %s refused: %v", dest, err)
		}
	}
}

// A unix listen makes the core create and remove "<path>.lock" and bind a socket at that
// path, so a push may only keep the socket this node stores; its ",mode" may change.
func TestPushedUnixListenMustBeTheStoredOne(t *testing.T) {
	push := pushGuardTestService(t)
	for i, listen := range []string{"/etc/x-ui/victim,0666", "@abstract-name", "[/etc/x-ui/victim,0666]", "[ @abstract-name ]", " /tmp/x.sock"} {
		_, _, err := push.AddInbound(&model.Inbound{Enable: true, Port: 44450 + i, Listen: listen, Protocol: model.VLESS, Settings: `{"clients":[]}`})
		if want := "may not set listen to"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("pushed listen %q = %v, want %q", listen, err, want)
		}
	}

	for i, listen := range []string{"[::1]", " 0.0.0.0 "} {
		if _, _, err := push.AddInbound(&model.Inbound{Enable: true, Port: 44455 + i, Listen: listen, Protocol: model.VLESS, Settings: `{"clients":[]}`}); err != nil {
			t.Errorf("pushed IP listen %q refused: %v", listen, err)
		}
	}

	seedInboundConflict(t, "socket", "/dev/shm/in.sock,0600", 0, model.VLESS, `{"network":"tcp"}`, `{"clients":[]}`)
	var stored model.Inbound
	if err := database.GetDB().Where("tag = ?", "socket").First(&stored).Error; err != nil {
		t.Fatalf("load the stored socket inbound: %v", err)
	}
	keep := stored
	keep.Listen = "/dev/shm/in.sock,0666"
	if _, _, err := push.UpdateInbound(&keep); err != nil {
		t.Fatalf("pushing the stored socket with another mode: %v", err)
	}
	moved := stored
	moved.Listen = "/dev/shm/other.sock"
	if _, _, err := push.UpdateInbound(&moved); err == nil || !strings.Contains(err.Error(), `may not set listen to "/dev/shm/other.sock"`) {
		t.Fatalf("a push moved the socket: %v", err)
	}
}

// The panel reads every certificate path when it builds the core config (and in unit mode
// copies it for the core), so a push may name only this node's own certificates.
func TestPushedCertificatePathMustBeTheNodesOwn(t *testing.T) {
	push := pushGuardTestService(t)
	own := filepath.Join(t.TempDir(), "panel.pem")
	if err := os.WriteFile(own, []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&SettingService{}).SetCertFile(own); err != nil {
		t.Fatal(err)
	}
	tls := func(port int, file string) *model.Inbound {
		return &model.Inbound{Enable: true, Port: port, Protocol: model.VLESS, Settings: `{"clients":[]}`,
			StreamSettings: `{"network":"tcp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"` + file + `","keyFile":"` + file + `"}]}}`}
	}
	for i, file := range []string{"/etc/shadow", "/root/cert/../.ssh/id_ed25519", "cert/../../etc/shadow"} {
		want := fmt.Sprintf("may not set certificate file to %q", file)
		if _, _, err := push.AddInbound(tls(44460+i, file)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("pushed %q = %v, want %q", file, err, want)
		}
	}
	for i, file := range []string{own, "/root/cert/vpn.example.com/fullchain.pem"} {
		if _, _, err := push.AddInbound(tls(44470+i, file)); err != nil {
			t.Errorf("pushed node certificate %q refused: %v", file, err)
		}
	}
}
