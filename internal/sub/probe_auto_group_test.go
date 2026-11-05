package sub

import (
	"reflect"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// probeAutoClash renders s1 with the Auto group on, after the probe ordering ran.
func probeAutoClash(t *testing.T, remark string, routing bool, rules string) (string, clashDoc) {
	t.Helper()
	svc := NewSubClashService(routing, rules, false, NewSubService(remark))
	svc.autoGroup = autoGroupOn()
	out, doc := renderClash(t, svc)
	if err := validateClashRouteGraph(mustYAMLMap(t, out)); err != nil {
		t.Fatalf("invalid Clash graph: %v\n%s", err, out)
	}
	// mihomo rejects a profile in which a proxy and a group, or two proxies, share a name.
	seen := map[string]bool{}
	for _, name := range proxyNamesOf(doc) {
		if seen[name] {
			t.Fatalf("proxy name %q used twice:\n%s", name, out)
		}
		seen[name] = true
	}
	for _, g := range doc.Groups {
		if name, _ := g["name"].(string); seen[name] {
			t.Fatalf("group %q shares its name with a proxy:\n%s", name, out)
		}
	}
	return out, doc
}

// The probe ordering runs before the Clash groups are built: a hidden proxy never
// joins the Auto group, and Auto counts only what is left.
func TestProbeOrderWithClashAutoGroup(t *testing.T) {
	t.Run("hidden proxy is not an Auto member", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		seedSubInbound(t, "s1", "c", 4433, 3, probeTCPStream)
		w.down(w.a, "203.0.113.5")
		out, doc := probeAutoClash(t, "", false, "")
		if got := proxyNamesOf(doc); !reflect.DeepEqual(got, []string{"b-b@e", "c-c@e"}) {
			t.Fatalf("proxies = %v\n%s", got, out)
		}
		auto := clashGroup(doc, clashAutoGroupName)
		if auto == nil || !reflect.DeepEqual(groupMembers(t, auto), []string{"b-b@e", "c-c@e"}) {
			t.Fatalf("Auto group must list only the reachable proxies:\n%s", out)
		}
		if got := groupMembers(t, clashGroup(doc, "PROXY")); !reflect.DeepEqual(got, []string{"Auto", "b-b@e", "c-c@e", "DIRECT"}) {
			t.Fatalf("PROXY = %v\n%s", got, out)
		}
	})

	t.Run("one proxy left needs no Auto group", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		w.down(w.a, "203.0.113.5")
		out, doc := probeAutoClash(t, "", false, "")
		if clashGroup(doc, clashAutoGroupName) != nil {
			t.Fatalf("Auto group with a single remaining proxy:\n%s", out)
		}
		if got := groupMembers(t, clashGroup(doc, "PROXY")); !reflect.DeepEqual(got, []string{"b-b@e", "DIRECT"}) {
			t.Fatalf("PROXY = %v\n%s", got, out)
		}
	})

	t.Run("template routing to Auto keeps it with the one proxy left", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		w.down(w.a, "203.0.113.5")
		out, doc := probeAutoClash(t, "", true, "- DOMAIN-SUFFIX,example.org,Auto\n")
		auto := clashGroup(doc, clashAutoGroupName)
		if auto == nil || !reflect.DeepEqual(groupMembers(t, auto), []string{"b-b@e"}) {
			t.Fatalf("Auto group must exist for the template and hold only the reachable proxy:\n%s", out)
		}
		if got := groupMembers(t, clashGroup(doc, "PROXY")); !reflect.DeepEqual(got, []string{"b-b@e", "DIRECT"}) {
			t.Fatalf("PROXY = %v, want Auto kept out of the selector\n%s", got, out)
		}
		if got := proxyNamesOf(doc); !reflect.DeepEqual(got, []string{"b-b@e"}) {
			t.Fatalf("proxies = %v\n%s", got, out)
		}
	})

	t.Run("hidden proxy a custom rule names stays out of Auto", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		seedSubInbound(t, "s1", "c", 4433, 3, probeTCPStream)
		w.down(w.a, "203.0.113.5")
		rules := "- DOMAIN-SUFFIX,example.org,a-a@e\n"
		out, doc := probeAutoClash(t, "", true, rules)
		if got := proxyNamesOf(doc); !reflect.DeepEqual(got, []string{"b-b@e", "c-c@e", "a-a@e"}) {
			t.Fatalf("proxies = %v, want the named hidden proxy kept at the end\n%s", got, out)
		}
		auto := clashGroup(doc, clashAutoGroupName)
		if auto == nil || !reflect.DeepEqual(groupMembers(t, auto), []string{"b-b@e", "c-c@e"}) {
			t.Fatalf("Auto group must not test the hidden proxy:\n%s", out)
		}
	})

	t.Run("reorder keeps the down proxy last in Auto", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderReorder)
		seedSubInbound(t, "s1", "c", 4433, 3, probeTCPStream)
		w.down(w.a, "203.0.113.5")
		out, doc := probeAutoClash(t, "", false, "")
		auto := clashGroup(doc, clashAutoGroupName)
		if auto == nil || !reflect.DeepEqual(groupMembers(t, auto), []string{"b-b@e", "c-c@e", "a-a@e"}) {
			t.Fatalf("Auto group must try the reachable proxies first:\n%s", out)
		}
	})

	// A hidden proxy called "Auto" skipped the renaming the visible ones get; a template
	// mentioning Auto must reach the group, not bring the hidden proxy back under its name.
	t.Run("hidden proxy called Auto is not restored", func(t *testing.T) {
		w := newProbeSubWorld(t, service.SubProbeOrderHide)
		seedSubInbound(t, "s1", "c", 4433, 3, probeTCPStream)
		if err := database.GetDB().Model(w.a).Update("remark", "Auto").Error; err != nil {
			t.Fatal(err)
		}
		w.down(w.a, "203.0.113.5")
		out, doc := probeAutoClash(t, "{{INBOUND}}", true, "- DOMAIN-SUFFIX,example.org,Auto\n")
		if got := proxyNamesOf(doc); !reflect.DeepEqual(got, []string{"b", "c"}) {
			t.Fatalf("proxies = %v\n%s", got, out)
		}
		auto := clashGroup(doc, clashAutoGroupName)
		if auto == nil || !reflect.DeepEqual(groupMembers(t, auto), []string{"b", "c"}) {
			t.Fatalf("Auto group = %v\n%s", auto, out)
		}
	})
}
