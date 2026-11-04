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
	if got := UnitBlockers(plain, false); len(got) != 0 {
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
	got := UnitBlockers(blocked, false)
	want := []string{`"sock"`, `"tp" (TPROXY)`, `"tun0" (TUN)`, `"marked" (socket mark)`, `"out-mark" (socket mark)`}
	if len(got) != len(want) {
		t.Fatalf("got %d blockers %v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("blocker %d = %q, want it to name %s", i, got[i], w)
		}
	}

	// A drop-in granting CAP_NET_ADMIN lifts the capability blockers, not the socket path.
	got = UnitBlockers(blocked, true)
	if len(got) != 1 || !strings.Contains(got[0], "/var/run/xray.sock") {
		t.Fatalf("with CAP_NET_ADMIN = %v, want only the socket path", got)
	}
	if UnitBlockers(nil, false) != nil {
		t.Fatal("nil config")
	}
}
