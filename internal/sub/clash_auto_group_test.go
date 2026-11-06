package sub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	yaml "github.com/goccy/go-yaml"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func autoGroupOn() ClashAutoGroup {
	g := defaultClashAutoGroup()
	g.Enabled = true
	return g
}

// seedAutoGroupSub gives subscription s1 n VLESS proxies.
func seedAutoGroupSub(t *testing.T, n int) {
	t.Helper()
	seedSubDB(t)
	for i := range n {
		seedSubInbound(t, "s1", fmt.Sprintf("node%d", i+1), 4700+i, i+1, wsTLSStream)
	}
}

type clashDoc struct {
	Proxies []map[string]any `yaml:"proxies"`
	Groups  []map[string]any `yaml:"proxy-groups"`
	Rules   []string         `yaml:"rules"`
}

func renderClash(t *testing.T, svc *SubClashService) (string, clashDoc) {
	t.Helper()
	out, _, err := svc.GetClash("s1", "sub.example.com")
	if err != nil {
		t.Fatalf("GetClash: %v", err)
	}
	var doc clashDoc
	if out != "" {
		if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("decode clash yaml: %v\n%s", err, out)
		}
	}
	return out, doc
}

func clashGroup(doc clashDoc, name string) map[string]any {
	for _, g := range doc.Groups {
		if g["name"] == name {
			return g
		}
	}
	return nil
}

func groupMembers(t *testing.T, g map[string]any) []string {
	t.Helper()
	raw, _ := g["proxies"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("group %v member %#v is not a string", g["name"], v)
		}
		out = append(out, s)
	}
	return out
}

func proxyNamesOf(doc clashDoc) []string {
	names := make([]string, 0, len(doc.Proxies))
	for _, p := range doc.Proxies {
		names = append(names, p["name"].(string))
	}
	return names
}

func TestClashAutoGroupByProxyCount(t *testing.T) {
	tests := []struct {
		name     string
		proxies  int
		wantAuto bool
	}{
		{name: "no proxies", proxies: 0},
		{name: "one proxy needs no choice", proxies: 1},
		{name: "two proxies", proxies: 2, wantAuto: true},
		{name: "five proxies", proxies: 5, wantAuto: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedAutoGroupSub(t, tt.proxies)
			svc := NewSubClashService(false, "", false, NewSubService(""))
			svc.autoGroup = autoGroupOn()
			out, doc := renderClash(t, svc)
			if tt.proxies == 0 {
				if out != "" {
					t.Fatalf("empty subscription rendered:\n%s", out)
				}
				return
			}
			names := proxyNamesOf(doc)
			if len(names) != tt.proxies {
				t.Fatalf("proxies = %v, want %d", names, tt.proxies)
			}
			proxy := clashGroup(doc, "PROXY")
			if proxy == nil || proxy["type"] != "select" {
				t.Fatalf("PROXY select group missing:\n%s", out)
			}
			auto := clashGroup(doc, "Auto")
			if !tt.wantAuto {
				if auto != nil || len(doc.Groups) != 1 {
					t.Fatalf("single proxy got an Auto group:\n%s", out)
				}
				if got, want := groupMembers(t, proxy), append(names, "DIRECT"); !reflect.DeepEqual(got, want) {
					t.Fatalf("PROXY = %v, want %v", got, want)
				}
				return
			}
			if auto == nil {
				t.Fatalf("Auto group missing:\n%s", out)
			}
			wantSelect := append([]string{"Auto"}, names...)
			wantSelect = append(wantSelect, "DIRECT")
			if got := groupMembers(t, proxy); !reflect.DeepEqual(got, wantSelect) {
				t.Fatalf("PROXY = %v, want Auto first (the default), then every proxy and DIRECT: %v", got, wantSelect)
			}
			if got := groupMembers(t, auto); !reflect.DeepEqual(got, names) {
				t.Fatalf("Auto members = %v, want every proxy %v", got, names)
			}
			want := map[string]any{
				"type": "url-test", "url": "http://www.gstatic.com/generate_204",
				"interval": uint64(300), "tolerance": uint64(50), "lazy": true,
			}
			for key, value := range want {
				if !reflect.DeepEqual(auto[key], value) {
					t.Errorf("Auto %s = %#v (%T), want %#v", key, auto[key], auto[key], value)
				}
			}
			if !reflect.DeepEqual(doc.Rules, []string{"MATCH,PROXY"}) {
				t.Fatalf("rules = %v, want MATCH,PROXY kept", doc.Rules)
			}
		})
	}
}

func TestClashAutoGroupOffKeepsManualSelectOnly(t *testing.T) {
	seedAutoGroupSub(t, 3)
	svc := NewSubClashService(false, "", false, NewSubService(""))
	out, doc := renderClash(t, svc)
	if len(doc.Groups) != 1 || clashGroup(doc, "Auto") != nil {
		t.Fatalf("Auto group present while off:\n%s", out)
	}
	if got, want := groupMembers(t, clashGroup(doc, "PROXY")), append(proxyNamesOf(doc), "DIRECT"); !reflect.DeepEqual(got, want) {
		t.Fatalf("PROXY = %v, want %v", got, want)
	}
}

func TestClashAutoGroupFallbackModeAndCustomCheck(t *testing.T) {
	seedAutoGroupSub(t, 2)
	svc := NewSubClashService(false, "", false, NewSubService(""))
	svc.autoGroup = ClashAutoGroup{Enabled: true, Type: "fallback", URL: "https://cp.cloudflare.com/generate_204", Interval: 60, Tolerance: 80}.normalized()
	out, doc := renderClash(t, svc)
	auto := clashGroup(doc, "Auto")
	if auto == nil || auto["type"] != "fallback" || auto["url"] != "https://cp.cloudflare.com/generate_204" || auto["interval"] != uint64(60) {
		t.Fatalf("fallback Auto group wrong:\n%s", out)
	}
	if _, ok := auto["tolerance"]; ok {
		t.Fatalf("fallback group carries a url-test tolerance:\n%s", out)
	}
}

func TestClashAutoGroupNormalizedRepairsBadValues(t *testing.T) {
	got := ClashAutoGroup{Enabled: true, Type: "load-balance", URL: "javascript:alert(1)", Interval: 1, Tolerance: -5}.normalized()
	want := ClashAutoGroup{Enabled: true, Type: "url-test", URL: service.DefaultSubClashAutoGroupURL, Interval: 300, Tolerance: 50}
	if got != want {
		t.Fatalf("normalized = %+v, want %+v", got, want)
	}
}

func TestClashAutoGroupSkipsInfoNode(t *testing.T) {
	seedAutoGroupSub(t, 2)
	sub := NewSubService("{{EMAIL}}")
	sub.subInfoNodeEnable = true
	svc := NewSubClashService(false, "", false, sub)
	svc.autoGroup = autoGroupOn()
	out, doc := renderClash(t, svc)
	if len(doc.Proxies) != 3 {
		t.Fatalf("want info node plus 2 proxies:\n%s", out)
	}
	for _, member := range groupMembers(t, clashGroup(doc, "Auto")) {
		for _, p := range doc.Proxies {
			if p["name"] == member && p["type"] == "socks5" {
				t.Fatalf("Auto tests the info node %q:\n%s", member, out)
			}
		}
	}
	if got := len(groupMembers(t, clashGroup(doc, "Auto"))); got != 2 {
		t.Fatalf("Auto members = %d, want 2:\n%s", got, out)
	}
}

// mihomo rejects a profile where a proxy and a group share a name, so a server
// whose remark is "Auto" must give the name up to the Auto group.
func TestClashAutoGroupRenamesProxyCalledAuto(t *testing.T) {
	seedSubDB(t)
	seedSubInbound(t, "s1", "Auto", 4700, 1, wsTLSStream)
	seedSubInbound(t, "s1", "node2", 4701, 2, wsTLSStream)

	// {{INBOUND}} names each proxy after its inbound remark alone.
	off := NewSubClashService(false, "", false, NewSubService("{{INBOUND}}"))
	if _, doc := renderClash(t, off); !slicesContain(proxyNamesOf(doc), "Auto") {
		t.Fatalf("setup: with the group off a proxy should be called Auto, got %v", proxyNamesOf(doc))
	}

	on := NewSubClashService(false, "", false, NewSubService("{{INBOUND}}"))
	on.autoGroup = autoGroupOn()
	out, doc := renderClash(t, on)
	names := proxyNamesOf(doc)
	if slicesContain(names, "Auto") {
		t.Fatalf("a proxy kept the Auto group's name, mihomo would reject the profile:\n%s", out)
	}
	auto := clashGroup(doc, "Auto")
	if auto == nil || !reflect.DeepEqual(groupMembers(t, auto), names) {
		t.Fatalf("Auto group must list the renamed proxies %v:\n%s", names, out)
	}
}

func TestClashAutoGroupLocalTemplates(t *testing.T) {
	tests := []struct {
		name      string
		proxies   int
		auto      ClashAutoGroup
		rules     string
		check     func(t *testing.T, out string, doc clashDoc)
		wantValid bool
	}{
		{
			name: "rule lines may route to Auto", proxies: 2, auto: autoGroupOn(),
			rules: "DOMAIN-SUFFIX,video.example,Auto",
			check: func(t *testing.T, out string, doc clashDoc) {
				if clashGroup(doc, "Auto") == nil || doc.Rules[0] != "DOMAIN-SUFFIX,video.example,Auto" || doc.Rules[len(doc.Rules)-1] != "MATCH,PROXY" {
					t.Fatalf("rule lines with Auto:\n%s", out)
				}
			},
		},
		{
			name: "a rule to Auto keeps a single-proxy subscription valid", proxies: 1, auto: autoGroupOn(),
			rules: "MATCH,Auto",
			check: func(t *testing.T, out string, doc clashDoc) {
				auto := clashGroup(doc, "Auto")
				if auto == nil || len(groupMembers(t, auto)) != 1 {
					t.Fatalf("Auto group missing for a template that routes to it:\n%s", out)
				}
				if slicesContain(groupMembers(t, clashGroup(doc, "PROXY")), "Auto") {
					t.Fatalf("single proxy: Auto must not be offered in PROXY:\n%s", out)
				}
			},
		},
		{
			name: "a rule to Auto keeps working while the group is off", proxies: 3,
			rules: "MATCH,Auto",
			check: func(t *testing.T, out string, doc clashDoc) {
				if clashGroup(doc, "Auto") == nil {
					t.Fatalf("Auto group missing for a template that routes to it:\n%s", out)
				}
				if slicesContain(groupMembers(t, clashGroup(doc, "PROXY")), "Auto") {
					t.Fatalf("Auto off: PROXY must stay manual:\n%s", out)
				}
			},
		},
		{
			name: "template without Auto and one proxy adds nothing", proxies: 1, auto: autoGroupOn(),
			rules: "GEOIP,private,DIRECT",
			check: func(t *testing.T, out string, doc clashDoc) {
				if len(doc.Groups) != 1 {
					t.Fatalf("unexpected groups:\n%s", out)
				}
			},
		},
		{
			name: "own proxy-groups are left exactly as written", proxies: 2, auto: autoGroupOn(),
			rules: "proxy-groups:\n  - name: PROXY\n    type: select\n    proxies: [DIRECT]\nrules:\n  - MATCH,PROXY\n",
			check: func(t *testing.T, out string, doc clashDoc) {
				if len(doc.Groups) != 1 || clashGroup(doc, "Auto") != nil || !reflect.DeepEqual(groupMembers(t, clashGroup(doc, "PROXY")), []string{"DIRECT"}) {
					t.Fatalf("user proxy-groups were changed:\n%s", out)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedAutoGroupSub(t, tt.proxies)
			svc := NewSubClashService(true, tt.rules, false, NewSubService(""))
			svc.autoGroup = tt.auto.normalized() // as NewSUBController does
			out, doc := renderClash(t, svc)
			if err := validateClashRouteGraph(mustYAMLMap(t, out)); err != nil {
				t.Fatalf("rendered config references something undefined: %v\n%s", err, out)
			}
			tt.check(t, out, doc)
		})
	}
}

const remoteClashTemplateWithAuto = `
proxy-groups:
  - name: Streaming
    type: select
    proxies: [Auto, PROXY, DIRECT]
rules:
  - DOMAIN-SUFFIX,video.example,Streaming
  - MATCH,PROXY
`

const remoteClashTemplateOwnAuto = `
proxy-groups:
  - name: Auto
    type: fallback
    url: https://cp.cloudflare.com/generate_204
    interval: 60
    include-all: true
rules:
  - MATCH,PROXY
`

const remoteClashTemplateAutoBackToProxy = `
proxy-groups:
  - name: Auto
    type: select
    proxies: [Streaming, DIRECT]
  - name: Streaming
    type: select
    proxies: [PROXY, DIRECT]
rules:
  - MATCH,PROXY
`

func TestClashAutoGroupRemoteTemplates(t *testing.T) {
	if _, _, err := normalizeClashRouting([]byte(remoteClashTemplateWithAuto)); err != nil {
		t.Fatalf("a remote template that uses Auto is refused at fetch time: %v", err)
	}
	tests := []struct {
		name     string
		template string
		proxies  int
		auto     ClashAutoGroup
		check    func(t *testing.T, out string, doc clashDoc)
	}{
		{
			name: "uses Auto with several proxies", template: remoteClashTemplateWithAuto, proxies: 2, auto: autoGroupOn(),
			check: func(t *testing.T, out string, doc clashDoc) {
				if clashGroup(doc, "Streaming") == nil || clashGroup(doc, "Auto") == nil || groupMembers(t, clashGroup(doc, "PROXY"))[0] != "Auto" {
					t.Fatalf("remote template with Auto:\n%s", out)
				}
			},
		},
		{
			name: "uses Auto with one proxy", template: remoteClashTemplateWithAuto, proxies: 1, auto: autoGroupOn(),
			check: func(t *testing.T, out string, doc clashDoc) {
				if clashGroup(doc, "Auto") == nil {
					t.Fatalf("Auto missing for a template that routes to it:\n%s", out)
				}
			},
		},
		{
			name: "uses Auto while it is off", template: remoteClashTemplateWithAuto, proxies: 2,
			check: func(t *testing.T, out string, doc clashDoc) {
				if clashGroup(doc, "Auto") == nil || slicesContain(groupMembers(t, clashGroup(doc, "PROXY")), "Auto") {
					t.Fatalf("Auto off with a template using it:\n%s", out)
				}
			},
		},
		{
			name: "defines its own Auto", template: remoteClashTemplateOwnAuto, proxies: 2, auto: autoGroupOn(),
			check: func(t *testing.T, out string, doc clashDoc) {
				count := 0
				for _, g := range doc.Groups {
					if g["name"] == "Auto" {
						count++
						if g["type"] != "fallback" || g["interval"] != uint64(60) {
							t.Fatalf("template's own Auto was replaced:\n%s", out)
						}
					}
				}
				if count != 1 {
					t.Fatalf("Auto groups = %d, want the template's one:\n%s", count, out)
				}
				if groupMembers(t, clashGroup(doc, "PROXY"))[0] != "Auto" {
					t.Fatalf("the template's own Auto is not the default in PROXY:\n%s", out)
				}
			},
		},
		{
			name: "own Auto leads back to PROXY", template: remoteClashTemplateAutoBackToProxy, proxies: 2, auto: autoGroupOn(),
			check: func(t *testing.T, out string, doc clashDoc) {
				if !slicesContain(groupMembers(t, clashGroup(doc, "Auto")), "Streaming") {
					t.Fatalf("template's own Auto was replaced:\n%s", out)
				}
				if slicesContain(groupMembers(t, clashGroup(doc, "PROXY")), "Auto") {
					t.Fatalf("PROXY -> Auto -> Streaming -> PROXY is a loop Mihomo refuses:\n%s", out)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedAutoGroupSub(t, tt.proxies)
			oldResolver := routingSourceResolver
			t.Cleanup(func() { routingSourceResolver = oldResolver })
			routingSourceResolver = newRemoteRoutingResolver(remoteRoutingTestClient(func(*http.Request) (*http.Response, error) {
				return remoteRoutingResponse(http.StatusOK, tt.template), nil
			}), false)
			const source = "https://example.com/mihomo/auto.yaml"
			primeRemoteRouting(t, routingSourceResolver, remoteRoutingClash, source)
			svc := NewSubClashService(true, source, false, NewSubService(""))
			svc.autoGroup = tt.auto.normalized() // as NewSUBController does
			out, doc := renderClash(t, svc)
			tt.check(t, out, doc)
		})
	}
}

func TestClashAutoGroupControllerOption(t *testing.T) {
	seedAutoGroupSub(t, 2)
	gin.SetMode(gin.TestMode)
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			router := gin.New()
			NewSUBController(router.Group("/"), WithSUBClashEnabled(true), WithSUBClashAutoGroup(ClashAutoGroup{Enabled: on}))
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "http://sub.example.com/clash/s1", nil))
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", resp.Code, resp.Body.String())
			}
			if got := strings.Contains(resp.Body.String(), "type: url-test"); got != on {
				t.Fatalf("Auto group present = %v, want %v:\n%s", got, on, resp.Body.String())
			}
		})
	}
}

func TestLoadClashAutoGroupReadsSettings(t *testing.T) {
	seedSubDB(t)
	settings := &service.SettingService{}
	all, err := settings.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	if got := loadClashAutoGroup(settings); got != autoGroupOn() {
		t.Fatalf("defaults loaded as %+v, want %+v", got, autoGroupOn())
	}
	all.SubClashAutoGroup = false
	all.SubClashAutoGroupType = "fallback"
	all.SubClashAutoGroupUrl = "https://cp.cloudflare.com/generate_204"
	all.SubClashAutoGroupInterval = 90
	all.SubClashAutoGroupTolerance = 10
	if err := settings.UpdateAllSetting(all, service.SecretClears{}); err != nil {
		t.Fatal(err)
	}
	want := ClashAutoGroup{Type: "fallback", URL: "https://cp.cloudflare.com/generate_204", Interval: 90, Tolerance: 10}
	if got := loadClashAutoGroup(settings); got != want {
		t.Fatalf("loaded %+v, want %+v", got, want)
	}
}

func mustYAMLMap(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func slicesContain(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
