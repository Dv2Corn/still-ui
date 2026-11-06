package xray

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

func inbound(tag, protocol, listen, stream string) InboundConfig {
	ib := InboundConfig{Tag: tag, Protocol: protocol}
	if listen != "" {
		ib.Listen = json_util.RawMessage(`"` + listen + `"`)
	}
	if stream != "" {
		ib.StreamSettings = json_util.RawMessage(stream)
	}
	return ib
}

func TestUnitBlockers(t *testing.T) {
	plain := &Config{
		InboundConfigs: []InboundConfig{
			inbound("api", "tunnel", "127.0.0.1", ""),
			inbound("vless", "vless", "", `{"network":"tcp","security":"reality","sockopt":{"tproxy":"off"}}`),
			inbound("fallback", "vless", "/dev/shm/xray.sock,0666", ""),
			inbound("run", "trojan", "/run/still-xray/t.sock", ""),
		},
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"}]`),
	}
	if got := UnitBlockers(plain, UnitCaps{}); len(got) != 0 {
		t.Fatalf("an ordinary config is blocked: %v", got)
	}

	blocked := &Config{
		InboundConfigs: []InboundConfig{
			inbound("sock", "vless", "/var/run/xray.sock", ""),
			inbound("tp", "tunnel", "", `{"sockopt":{"tproxy":"tproxy"}}`),
			inbound("tun0", "tun", "", ""),
			inbound("marked", "vless", "", `{"sockopt":{"mark":255}}`),
			inbound("redirect", "tunnel", "", `{"sockopt":{"tproxy":"redirect"}}`),
		},
		OutboundConfigs: json_util.RawMessage(`[{"tag":"out-mark","protocol":"freedom","streamSettings":{"sockopt":{"mark":2}}}]`),
	}
	got := UnitBlockers(blocked, UnitCaps{})
	want := []string{`"sock"`, `"tp" (TPROXY)`, `"tun0" (TUN)`, `"marked" (socket mark)`, `"out-mark" (socket mark)`}
	if len(got) != len(want) {
		t.Fatalf("got %d blockers %v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("blocker %d = %q, want it to name %s", i, got[i], w)
		}
	}

	// The shipped unit grants CAP_NET_ADMIN: marks and TPROXY run, TUN still lacks /dev/net/tun.
	got = UnitBlockers(blocked, UnitCaps{NetAdmin: true})
	if len(got) != 2 || !strings.Contains(got[0], "/var/run/xray.sock") || !strings.Contains(got[1], `"tun0" (TUN) needs /dev/net/tun`) {
		t.Fatalf("with CAP_NET_ADMIN = %v, want the socket path and TUN", got)
	}
	// A drop-in with PrivateDevices=no as well: only the socket path is left.
	got = UnitBlockers(blocked, UnitCaps{NetAdmin: true, Devices: true})
	if len(got) != 1 || !strings.Contains(got[0], "/var/run/xray.sock") {
		t.Fatalf("with CAP_NET_ADMIN and /dev = %v, want only the socket path", got)
	}
	if UnitBlockers(nil, UnitCaps{}) != nil {
		t.Fatal("nil config")
	}
}
