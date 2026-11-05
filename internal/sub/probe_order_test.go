package sub

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const probeTCPStream = `{"network":"tcp","security":"none"}`

// probeSubWorld is one subscription "s1" with inbounds A (port 4431) and B (port 4432) on
// 203.0.113.5, and one fresh agent that checks both plus a healthy control endpoint.
type probeSubWorld struct {
	t     *testing.T
	a, b  *model.Inbound
	agent *model.ReachProbe
}

func newProbeSubWorld(t *testing.T, mode string) *probeSubWorld {
	t.Helper()
	seedSubDB(t)
	resetProbeVerdictCache()
	t.Cleanup(resetProbeVerdictCache)
	w := &probeSubWorld{t: t}
	w.a = seedSubInbound(t, "s1", "a", 4431, 1, probeTCPStream)
	w.b = seedSubInbound(t, "s1", "b", 4432, 2, probeTCPStream)
	setSubSetting(t, "subProbeOrder", mode)
	w.agent = &model.ReachProbe{
		Name: "home", TokenHash: "h", Enabled: true, ClientEmail: "still-probe-x", InboundIds: []int{w.a.Id, w.b.Id, 999},
		TestURL: service.ReachProbeDefaultTestURL, IntervalSec: 600, LastSeenAt: time.Now().UnixMilli(),
	}
	if err := database.GetDB().Create(w.agent).Error; err != nil {
		t.Fatal(err)
	}
	w.state("999@control.example.com:443", 0)
	return w
}

func setSubSetting(t *testing.T, key, value string) {
	t.Helper()
	row := &model.Setting{Key: key}
	if err := database.GetDB().Where("key = ?", key).Assign(model.Setting{Value: value}).FirstOrCreate(row).Error; err != nil {
		t.Fatal(err)
	}
}

// state records the agent's latest check of key with that many failures in a row.
func (w *probeSubWorld) state(key string, fails int) {
	w.t.Helper()
	status := service.ReachStatusOk
	if fails >= service.ReachProbeFailThreshold {
		status = service.ReachStatusFail
	}
	db := database.GetDB()
	if err := db.Create(&model.ReachProbeTarget{ProbeId: w.agent.Id, TargetKey: key, Status: status, ConsecutiveFails: fails}).Error; err != nil {
		w.t.Fatal(err)
	}
	if err := db.Create(&model.ReachProbeResult{ProbeId: w.agent.Id, TargetKey: key, Ok: fails == 0, At: time.Now().UnixMilli()}).Error; err != nil {
		w.t.Fatal(err)
	}
}

func (w *probeSubWorld) down(ib *model.Inbound, host string) {
	w.state(fmt.Sprintf("%d@%s:%d", ib.Id, host, ib.Port), service.ReachProbeFailThreshold)
}

func (w *probeSubWorld) rawLinks() []string {
	w.t.Helper()
	req := NewSubService("").ForRequest("req.example.com")
	req.subscriptionBody = true
	links, _, _, _, err := req.getSubs("s1")
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, l := range links {
		out = append(out, splitLinkLines(l)...)
	}
	return out
}

// linkPorts names each raw link by its host and port, in subscription order.
func linkPorts(links []string) string {
	var out []string
	for _, l := range links {
		at := strings.LastIndex(l, "@")
		end := strings.IndexAny(l[at:], "?#")
		out = append(out, l[at+1:at+end])
	}
	return strings.Join(out, " ")
}

func TestProbeOrderRawReorderMovesDownLinksToTheEnd(t *testing.T) {
	w := newProbeSubWorld(t, service.SubProbeOrderReorder)
	w.down(w.a, "203.0.113.5")
	if got := linkPorts(w.rawLinks()); got != "203.0.113.5:4432 203.0.113.5:4431" {
		t.Fatalf("order = %q, want the unreachable 4431 last", got)
	}
}

func TestProbeOrderRawHideDropsDownLinks(t *testing.T) {
	w := newProbeSubWorld(t, service.SubProbeOrderHide)
	w.down(w.a, "203.0.113.5")
	if got := linkPorts(w.rawLinks()); got != "203.0.113.5:4432" {
		t.Fatalf("links = %q, want only 4432", got)
	}
}

// The last link is never taken away: with every link down the normal list is served.
func TestProbeOrderNeverHidesTheLastLink(t *testing.T) {
	w := newProbeSubWorld(t, service.SubProbeOrderHide)
	w.down(w.a, "203.0.113.5")
	w.down(w.b, "203.0.113.5")
	if got := linkPorts(w.rawLinks()); got != "203.0.113.5:4431 203.0.113.5:4432" {
		t.Fatalf("links = %q, want both in the normal order", got)
	}
}

func TestProbeOrderOffOrUnprobedKeepsTheNormalList(t *testing.T) {
	w := newProbeSubWorld(t, service.SubProbeOrderOff)
	w.down(w.a, "203.0.113.5")
	if got := linkPorts(w.rawLinks()); got != "203.0.113.5:4431 203.0.113.5:4432" {
		t.Fatalf("mode off: %q", got)
	}
	setSubSetting(t, "subProbeOrder", service.SubProbeOrderHide)
	resetProbeVerdictCache()
	w.state(fmt.Sprintf("%d@other.example.com:%d", w.b.Id, w.b.Port), service.ReachProbeFailThreshold)
	if got := linkPorts(w.rawLinks()); got != "203.0.113.5:4432" {
		t.Fatalf("hide: %q, want 4431 hidden and 4432 (no data for its real endpoint) kept", got)
	}
}

// The panel's own link and QR views and the probe job see every link, whatever the probes say.
func TestProbeOrderLeavesPanelViewsAlone(t *testing.T) {
	w := newProbeSubWorld(t, service.SubProbeOrderHide)
	w.down(w.a, "203.0.113.5")
	links, err := NewLinkProvider().SubLinksForSubId("req.example.com", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got := linkPorts(links); got != "203.0.113.5:4431 203.0.113.5:4432" {
		t.Fatalf("panel view = %q, want every link", got)
	}
}

// Each endpoint of a multi-host inbound is judged alone: only the blocked one moves.
func TestProbeOrderJudgesEachHostEndpoint(t *testing.T) {
	w := newProbeSubWorld(t, service.SubProbeOrderReorder)
	seedHost(t, &model.Host{InboundId: w.a.Id, GroupId: "g1", SortOrder: 1, Remark: "A1", Address: "a1.example.com"})
	seedHost(t, &model.Host{InboundId: w.a.Id, GroupId: "g2", SortOrder: 2, Remark: "A2", Address: "a2.example.com"})
	w.down(w.a, "a1.example.com")
	if got := linkPorts(w.rawLinks()); got != "a2.example.com:4431 203.0.113.5:4432 a1.example.com:4431" {
		t.Fatalf("order = %q", got)
	}
}

func probeClashDoc(t *testing.T, routing bool, rules string) map[string]any {
	t.Helper()
	out, _, err := NewSubClashService(routing, rules, false, NewSubService("")).GetClash("s1", "req.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := validateClashRouteGraph(doc); err != nil {
		t.Fatalf("invalid Clash graph: %v\n%s", err, out)
	}
	return doc
}

func clashServers(doc map[string]any) string {
	var out []string
	proxies, _ := doc["proxies"].([]any)
	for _, p := range proxies {
		m := p.(map[string]any)
		out = append(out, fmt.Sprintf("%v", m["port"]))
	}
	return strings.Join(out, " ")
}

func probeClashGroup(doc map[string]any, name string) []any {
	groups, _ := doc["proxy-groups"].([]any)
	for _, g := range groups {
		if m := g.(map[string]any); m["name"] == name {
			return m["proxies"].([]any)
		}
	}
	return nil
}

func TestProbeOrderClash(t *testing.T) {
	t.Run("reorder", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderReorder)
		w.down(w.a, "203.0.113.5")
		doc := probeClashDoc(t, false, "")
		if got := clashServers(doc); got != "4432 4431" {
			t.Fatalf("proxies = %q", got)
		}
		if group := probeClashGroup(doc, "PROXY"); fmt.Sprint(group) != "[b-b@e a-a@e DIRECT]" {
			t.Fatalf("PROXY group must start with the reachable proxy: %v", group)
		}
	})
	t.Run("hide", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		w.down(w.a, "203.0.113.5")
		doc := probeClashDoc(t, false, "")
		if got := clashServers(doc); got != "4432" {
			t.Fatalf("proxies = %q", got)
		}
		if group := probeClashGroup(doc, "PROXY"); fmt.Sprint(group) != "[b-b@e DIRECT]" {
			t.Fatalf("PROXY group = %v, want the hidden proxy left out", group)
		}
	})
	t.Run("all down", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		w.down(w.a, "203.0.113.5")
		w.down(w.b, "203.0.113.5")
		if got := clashServers(probeClashDoc(t, false, "")); got != "4431 4432" {
			t.Fatalf("proxies = %q, want the normal list", got)
		}
	})
	// A custom group naming the hidden proxy keeps it, or Mihomo would reject the whole profile.
	t.Run("custom routing names the hidden proxy", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		w.down(w.a, "203.0.113.5")
		rules := "proxy-groups:\n  - name: PROXY\n    type: select\n    proxies: [a-a@e, b-b@e]\n"
		if got := clashServers(probeClashDoc(t, true, rules)); got != "4432 4431" {
			t.Fatalf("proxies = %q, want the named proxy kept at the end", got)
		}
	})
	// Legacy Clash drops VLESS proxies; the hidden VMess one is then the only link left to serve.
	t.Run("legacy keeps a link", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		vmess := `{"clients":[{"id":"11111111-2222-4333-8444-000000004431","email":"a@e","subId":"s1","enable":true,"security":"auto"}]}`
		if err := database.GetDB().Model(w.a).Updates(map[string]any{"protocol": model.VMESS, "settings": vmess}).Error; err != nil {
			t.Fatal(err)
		}
		w.down(w.a, "203.0.113.5")
		out, _, err := NewSubClashService(false, "", false, NewSubService("")).GetClashLegacy("s1", "req.example.com")
		if err != nil {
			t.Fatalf("legacy Clash with every visible proxy unsupported: %v", err)
		}
		if !strings.Contains(out, "port: 4431") || strings.Contains(out, "port: 4432") {
			t.Fatalf("legacy profile must fall back to the hidden proxy:\n%s", out)
		}
	})
	t.Run("custom routing without names still hides", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		w.down(w.a, "203.0.113.5")
		if got := clashServers(probeClashDoc(t, true, "- DOMAIN-SUFFIX,example.org,DIRECT\n")); got != "4432" {
			t.Fatalf("proxies = %q", got)
		}
	})
}

func jsonPorts(t *testing.T) string {
	t.Helper()
	out, _, err := NewSubJsonService("", "", "", "", NewSubService("")).GetJson("s1", "req.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	var ports []string
	for _, doc := range parseSubJsonDocs(t, out) {
		if doc["remarks"] == "auto" {
			ports = append(ports, "balancer:"+strings.Join(docOutboundTags(doc), ","))
			continue
		}
		outbounds := doc["outbounds"].([]any)
		settings := outbounds[0].(map[string]any)["settings"].(map[string]any)
		ports = append(ports, fmt.Sprintf("%v", settings["port"]))
	}
	return strings.Join(ports, " ")
}

func TestProbeOrderJSON(t *testing.T) {
	t.Run("reorder", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderReorder)
		w.down(w.a, "203.0.113.5")
		if got := jsonPorts(t); got != "4432 4431" {
			t.Fatalf("documents = %q", got)
		}
	})
	t.Run("hide keeps the balancer valid", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		seedSubBalancer(t, &model.SubBalancer{Remark: "auto", Strategy: "random", InboundIds: []int{w.a.Id, w.b.Id}, SortOrder: 5, Enabled: true})
		w.down(w.a, "203.0.113.5")
		if got := jsonPorts(t); got != "4432 balancer:bal-1-vless,direct,block" {
			t.Fatalf("documents = %q, want 4431 out of the list and out of the balancer", got)
		}
	})
	t.Run("all down", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		w.down(w.a, "203.0.113.5")
		w.down(w.b, "203.0.113.5")
		if got := jsonPorts(t); got != "4431 4432" {
			t.Fatalf("documents = %q, want the normal list", got)
		}
	})
}
