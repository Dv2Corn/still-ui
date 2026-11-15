package service

import (
	"fmt"
	"net"
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

// A relay target is judged by its address, not by its port: with the panel and the
// subscription server on 443, a public REALITY target on 443 (the usual kind) passes, while
// loopback and this host's own addresses are refused on any port.
func TestPushedRelayTargetIsJudgedByAddressNotPort(t *testing.T) {
	push := pushGuardTestService(t)
	settings := &SettingService{}
	if err := settings.SetPort(443); err != nil {
		t.Fatal(err)
	}
	if err := settings.setInt("subPort", 443); err != nil {
		t.Fatal(err)
	}
	reality := func(port int, target string) *model.Inbound {
		return &model.Inbound{
			Enable: true, Port: port, Protocol: model.VLESS, Settings: `{"clients":[]}`,
			StreamSettings: `{"network":"tcp","security":"reality","realitySettings":{"target":"` + target + `"}}`,
		}
	}
	for i, target := range []string{"www.nvidia.com:443", "1.1.1.1:443", "sub.example.com:2096"} {
		if _, _, err := push.AddInbound(reality(44420+i, target)); err != nil && strings.Contains(err.Error(), "may not set") {
			t.Errorf("public REALITY target %q refused: %v", target, err)
		}
	}
	refused := []string{"127.0.0.1:443", "[::1]:443", "localhost:443"}
	if own := firstOwnPublicIP(); own != "" {
		refused = append(refused, net.JoinHostPort(own, "443"))
	}
	for i, target := range refused {
		want := fmt.Sprintf("may not set REALITY target to %q", target)
		if _, _, err := push.AddInbound(reality(44430+i, target)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("AddInbound(%q) = %v, want %q", target, err, want)
		}
	}
	// Go's dialer reads a leading zero as the same port; such a spelling is never public.
	fb := &model.Inbound{Enable: true, Port: 44440, Protocol: model.VLESS, Settings: `{"clients":[],"fallbacks":[{"dest":"www.example.com:0443"}]}`, StreamSettings: `{"network":"tcp"}`}
	if _, _, err := push.AddInbound(fb); err == nil || !strings.Contains(err.Error(), `may not set fallback dest to "www.example.com:0443"`) {
		t.Errorf("AddInbound(leading-zero port) = %v", err)
	}
}

// firstOwnPublicIP returns a non-loopback address of this host's interfaces, if any.
func firstOwnPublicIP() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			return n.IP.String()
		}
	}
	return ""
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
		_, _, err := push.AddInbound(&model.Inbound{
			Enable: true, Port: port, Protocol: model.VLESS,
			Settings: `{"clients":[],"fallbacks":[{"dest":` + dest + `}]}`, StreamSettings: `{"network":"tcp"}`,
		})
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
		return &model.Inbound{
			Enable: true, Port: port, Protocol: model.VLESS, Settings: `{"clients":[]}`,
			StreamSettings: `{"network":"tcp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"` + file + `","keyFile":"` + file + `"}]}}`,
		}
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
