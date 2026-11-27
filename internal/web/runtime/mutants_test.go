package runtime

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestLocalWithAPITreatsZeroPortAsStopped(t *testing.T) {
	local := NewLocal(LocalDeps{APIPort: func() int { return 0 }})
	err := local.withAPI(func(*xray.XrayAPI) error {
		t.Fatal("API callback ran although the local core has no API port")
		return nil
	})
	if err == nil || err.Error() != "local xray is not running" {
		t.Fatalf("withAPI error = %v, want local xray is not running", err)
	}
}

func TestManagerRemoteForCopiesInboundTags(t *testing.T) {
	m := NewManager(LocalDeps{})
	n := &model.Node{Id: 10, Scheme: "https", Address: "node.example.com", Port: 2053, InboundTags: []string{"a"}}
	if _, err := m.RemoteFor(n); err != nil {
		t.Fatalf("RemoteFor: %v", err)
	}
	n.InboundTags[0] = "changed"
	if got := m.remotes[n.Id].node.InboundTags; len(got) != 1 || got[0] != "a" {
		t.Fatalf("cached tags = %#v, want an independent copy [a]", got)
	}
}

func TestTruncateNodeMessageBounds(t *testing.T) {
	exact := strings.Repeat("a", errBodyDiagBytes)
	if got := truncateNodeMessage(exact); got != exact {
		t.Fatalf("a message of exactly %d bytes must be kept whole", errBodyDiagBytes)
	}
	// Byte errBodyDiagBytes falls inside a two-byte rune: the cut moves back to its start.
	got := truncateNodeMessage("a" + strings.Repeat("я", errBodyDiagBytes))
	if want := "a" + strings.Repeat("я", errBodyDiagBytes/2-1) + "... (truncated)"; got != want {
		t.Fatalf("cut inside a rune: got %d bytes, want %d", len(got), len(want))
	}
	if got := truncateNodeMessage(strings.Repeat("\x80", errBodyDiagBytes+10)); got != "... (truncated)" {
		t.Fatalf("no rune start at all: got %q", got)
	}
}

func TestNoCrossHostRedirectLimits(t *testing.T) {
	req := func(host string) *http.Request { return &http.Request{URL: &url.URL{Host: host}} }
	via := func(n int) []*http.Request {
		out := make([]*http.Request, n)
		for i := range out {
			out[i] = req("node:2053")
		}
		return out
	}
	if err := NoCrossHostRedirect(req("NODE:2053"), via(9)); err != nil {
		t.Fatalf("9th same-host redirect: %v", err)
	}
	if err := NoCrossHostRedirect(req("node:2053"), via(10)); err != http.ErrUseLastResponse {
		t.Fatalf("10th redirect: %v, want ErrUseLastResponse", err)
	}
	if err := NoCrossHostRedirect(req("other:2053"), via(1)); err != http.ErrUseLastResponse {
		t.Fatalf("cross-host redirect: %v, want ErrUseLastResponse", err)
	}
}

func TestWireInboundTagAndResetDay(t *testing.T) {
	ib := &model.Inbound{Tag: nodeInboundTagPrefix(0) + "x"}
	v := wireInbound(ib, 0)
	if v.Get("tag") != ib.Tag {
		t.Fatalf("remote node id 0 must keep the tag, got %q", v.Get("tag"))
	}
	if _, ok := v["trafficResetDay"]; ok {
		t.Fatal("trafficResetDay sent for a zero value")
	}
	ib = &model.Inbound{Tag: nodeInboundTagPrefix(3) + "x", TrafficResetDay: 1}
	v = wireInbound(ib, 3)
	if v.Get("tag") != "x" || v.Get("trafficResetDay") != "1" {
		t.Fatalf("tag=%q trafficResetDay=%q, want x and 1", v.Get("tag"), v.Get("trafficResetDay"))
	}
}

func TestLocalUpdateInboundKeepsPlainXrayOnTheXrayPath(t *testing.T) {
	var restarts, reconciles int
	local := NewLocal(LocalDeps{APIPort: func() int { return 0 }, SetNeedRestart: func() { restarts++ }, RequestReconcile: func() { reconciles++ }})
	err := local.UpdateInbound(context.Background(),
		&model.Inbound{Id: 1, Tag: "a", Protocol: model.VLESS, Enable: true, Settings: "{}"},
		&model.Inbound{Id: 1, Tag: "a", Protocol: model.VLESS, Enable: true, Settings: "{}"})
	if err == nil || err.Error() != "local xray is not running" {
		t.Fatalf("vless update error = %v, want the xray API path (local xray is not running)", err)
	}
	if restarts != 0 || reconciles != 0 {
		t.Fatalf("vless update took a sidecar path: restarts=%d reconciles=%d", restarts, reconciles)
	}
}

func TestLocalAddAmneziaWGFlagsXrayResync(t *testing.T) {
	ib := datapathInbound(t, 4183, 58733, false)
	t.Cleanup(func() { amneziawgnet.GetManager().Remove(ib.Id) })
	restarts := 0
	local := NewLocal(LocalDeps{APIPort: func() int { return 0 }, SetNeedRestart: func() { restarts++ }})
	if err := local.AddInbound(context.Background(), ib); err != nil {
		t.Fatalf("AddInbound: %v", err)
	}
	if restarts != 1 {
		t.Fatalf("SetNeedRestart called %d times, want 1", restarts)
	}
}

func TestLocalUpdateTuicToXrayAddsTheXrayInbound(t *testing.T) {
	local := NewLocal(LocalDeps{APIPort: func() int { return 0 }, RequestReconcile: func() {}})
	err := local.UpdateInbound(context.Background(),
		&model.Inbound{Id: 2, Tag: "t", Protocol: model.TUIC, Enable: true, Settings: "{}"},
		&model.Inbound{Id: 2, Tag: "t", Protocol: model.VLESS, Enable: true, Settings: "{}"})
	if err == nil || err.Error() != "local xray is not running" {
		t.Fatalf("tuic -> vless error = %v, want the xray API path (local xray is not running)", err)
	}
}
