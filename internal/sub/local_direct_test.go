package sub

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	yaml "github.com/goccy/go-yaml"
	xraygeodata "github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/sub/localdirect"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const localDirectXrayRules = `{"domain":["domain:telegram.org"],"outboundTag":"proxy","type":"field"},` +
	`{"domain":["domain:ru","domain:su","domain:xn--p1ai","geosite:category-ru"],"outboundTag":"direct","type":"field"},` +
	`{"ip":["10.0.0.0/8","100.64.0.0/10","127.0.0.0/8","169.254.0.0/16","172.16.0.0/12","192.168.0.0/16","::1/128","fc00::/7","fe80::/10"],"outboundTag":"direct","type":"field"},` +
	`{"ip":["91.108.0.0/16"],"outboundTag":"proxy","type":"field"},` +
	`{"ip":["geoip:ru"],"outboundTag":"direct","type":"field"}`

const xrayCatchAllRule = `{"network":"tcp,udp","outboundTag":"proxy","type":"field"}`

var localDirectClashRules = []any{
	"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
	"IP-CIDR,100.64.0.0/10,DIRECT,no-resolve",
	"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
	"IP-CIDR,169.254.0.0/16,DIRECT,no-resolve",
	"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
	"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
	"IP-CIDR6,::1/128,DIRECT,no-resolve",
	"IP-CIDR6,fc00::/7,DIRECT,no-resolve",
	"IP-CIDR6,fe80::/10,DIRECT,no-resolve",
	"DOMAIN-SUFFIX,telegram.org,PROXY",
	"IP-CIDR,91.108.0.0/16,PROXY,no-resolve",
	"DOMAIN-SUFFIX,ru,DIRECT",
	"DOMAIN-SUFFIX,su,DIRECT",
	"DOMAIN-SUFFIX,xn--p1ai,DIRECT",
	"GEOSITE,category-ru,DIRECT",
	"GEOIP,RU,DIRECT",
}

func localDirectTestPlan(t *testing.T) localdirect.Plan {
	t.Helper()
	return localdirect.Plan{
		Country:      "ru",
		ProxyDomains: []string{"telegram.org"},
		ProxyIPs:     []string{"91.108.0.0/16"},
		TLDs:         []string{"ru", "su", "xn--p1ai"},
		Geosite:      "category-ru",
		GeoIP:        true,
	}
}

func seedLocalDirectSub(t *testing.T) {
	t.Helper()
	seedSubDB(t)
	seedSubInbound(t, "s1", "raw", 4481, 1, `{"network":"tcp","security":"none"}`)
	seedSubProtocolInbound(t, "s1", "vmess", 4485, 2, `{"network":"tcp","security":"tls","tlsSettings":{"serverName":"vm.example.com"}}`, model.VMESS)
	gin.SetMode(gin.TestMode)
}

func newLocalDirectRouter(options ...SUBControllerOption) *gin.Engine {
	router := gin.New()
	NewSUBController(router.Group("/"), append([]SUBControllerOption{WithSUBJsonEnabled(true), WithSUBClashEnabled(true)}, options...)...)
	return router
}

func localDirectRouter(t *testing.T, options ...SUBControllerOption) *gin.Engine {
	t.Helper()
	seedLocalDirectSub(t)
	return newLocalDirectRouter(options...)
}

func localDirectGet(router *gin.Engine, path, userAgent string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://sub.example.com"+path, nil)
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func jsonSubRouting(t *testing.T, resp *httptest.ResponseRecorder) string {
	t.Helper()
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", resp.Code, resp.Body.String())
	}
	var configs []struct {
		Routing json.RawMessage `json:"routing"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &configs); err != nil || len(configs) != 2 {
		t.Fatalf("JSON subscription: %d configs, %v; body=%s", len(configs), err, resp.Body.String())
	}
	if string(configs[0].Routing) != string(configs[1].Routing) {
		t.Fatalf("the two configs route differently:\n%s\n%s", configs[0].Routing, configs[1].Routing)
	}
	var routing any
	if err := json.Unmarshal(configs[0].Routing, &routing); err != nil {
		t.Fatalf("routing: %v", err)
	}
	compact, err := json.Marshal(routing)
	if err != nil {
		t.Fatal(err)
	}
	return string(compact)
}

func clashSubRules(t *testing.T, resp *httptest.ResponseRecorder) []any {
	t.Helper()
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", resp.Code, resp.Body.String())
	}
	var config map[string]any
	if err := yaml.Unmarshal(resp.Body.Bytes(), &config); err != nil {
		t.Fatalf("Clash subscription: %v; body=%s", err, resp.Body.String())
	}
	rules, _ := config["rules"].([]any)
	return rules
}

func TestLocalDirectJSONSubscription(t *testing.T) {
	tests := []struct {
		name    string
		options []SUBControllerOption
		want    string
	}{
		{
			name: "rules sit ahead of the catch-all",
			want: `{"domainStrategy":"IPOnDemand","rules":[` + localDirectXrayRules + `,` + xrayCatchAllRule + `]}`,
		},
		{
			name:    "the operator's own rules still match first",
			options: []SUBControllerOption{WithSUBJsonRules(`[{"type":"field","domain":["domain:example.ru"],"outboundTag":"proxy"}]`)},
			want: `{"domainStrategy":"IPOnDemand","rules":[{"domain":["domain:example.ru"],"outboundTag":"proxy","type":"field"},` +
				localDirectXrayRules + `,` + xrayCatchAllRule + `]}`,
		},
		{
			name:    "an explicit routing profile replaces it",
			options: []SUBControllerOption{WithSUBJsonRoutingRules(`{"DirectSites":["domain:example.org"],"DomainStrategy":"AsIs"}`)},
			want: `{"domainStrategy":"AsIs","rules":[{"domain":["domain:example.org"],"outboundTag":"direct","type":"field"},` +
				xrayCatchAllRule + `]}`,
		},
		{
			name:    "a routing profile that cannot be read still replaces it",
			options: []SUBControllerOption{WithSUBJsonRoutingRules(`{"DirectSites":"not a list"}`)},
			want:    `{"domainStrategy":"AsIs","rules":[` + xrayCatchAllRule + `]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := localDirectRouter(t, append([]SUBControllerOption{WithSUBLocalDirect(localDirectTestPlan(t))}, tt.options...)...)
			if got := jsonSubRouting(t, localDirectGet(router, "/json/s1", "")); got != tt.want {
				t.Fatalf("routing:\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestLocalDirectClashSubscription(t *testing.T) {
	withoutGeosite := slices.DeleteFunc(slices.Clone(localDirectClashRules), func(rule any) bool { return rule == "GEOSITE,category-ru,DIRECT" })
	tests := []struct {
		name    string
		path    string
		options []SUBControllerOption
		want    []any
	}{
		{name: "mihomo", path: "/clash/s1", want: append(slices.Clone(localDirectClashRules), "MATCH,PROXY")},
		{name: "clash for windows has no GEOSITE rule", path: "/clash-legacy/s1", want: append(withoutGeosite, "MATCH,PROXY")},
		{
			name:    "the operator's own rules still match first",
			path:    "/clash/s1",
			options: []SUBControllerOption{WithSUBClashEnableRouting(true), WithSUBClashRules("DOMAIN,example.ru,PROXY")},
			want:    append(append([]any{"DOMAIN,example.ru,PROXY"}, localDirectClashRules...), "MATCH,PROXY"),
		},
		{
			name:    "a complete custom rule set replaces it",
			path:    "/clash/s1",
			options: []SUBControllerOption{WithSUBClashEnableRouting(true), WithSUBClashRules("DOMAIN,example.ru,DIRECT\nMATCH,PROXY")},
			want:    []any{"DOMAIN,example.ru,DIRECT", "MATCH,PROXY"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := localDirectRouter(t, append([]SUBControllerOption{WithSUBLocalDirect(localDirectTestPlan(t))}, tt.options...)...)
			if got := clashSubRules(t, localDirectGet(router, tt.path, "")); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("rules:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func decodeHappRouting(t *testing.T, header string) map[string]any {
	t.Helper()
	payload, ok := strings.CutPrefix(header, "happ://routing/onadd/")
	if !ok {
		t.Fatalf("Routing = %q, want a happ://routing/onadd/ deeplink", header)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("Routing payload: %v", err)
	}
	var profile map[string]any
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatalf("Routing profile: %v", err)
	}
	return profile
}

func TestLocalDirectRoutingHeader(t *testing.T) {
	const happUA = "Happ/3.2.1"
	wantProfile := map[string]any{
		"Name": "Local direct RU", "GlobalProxy": "true", "RouteOrder": "block-proxy-direct", "DomainStrategy": "IPIfNonMatch",
		"DirectSites": []any{"domain:ru", "domain:su", "domain:xn--p1ai", "geosite:category-ru"},
		"DirectIp": []any{
			"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16",
			"::1/128", "fc00::/7", "fe80::/10", "geoip:ru",
		},
		"ProxySites": []any{"domain:telegram.org"}, "ProxyIp": []any{"91.108.0.0/16"},
		"BlockSites": []any{}, "BlockIp": []any{},
	}

	t.Run("happ gets the profile switched on", func(t *testing.T) {
		router := localDirectRouter(t, WithSUBLocalDirect(localDirectTestPlan(t)))
		resp := localDirectGet(router, "/sub/s1", happUA)
		if got := decodeHappRouting(t, resp.Header().Get("Routing")); !reflect.DeepEqual(got, wantProfile) {
			t.Fatalf("profile:\n got %#v\nwant %#v", got, wantProfile)
		}
		if got := resp.Header().Get("Routing-Enable"); got != "true" {
			t.Fatalf("Routing-Enable = %q, want true", got)
		}
	})
	t.Run("happ-managed panels do not switch it back off", func(t *testing.T) {
		router := localDirectRouter(t, WithSUBLocalDirect(localDirectTestPlan(t)), WithSUBHappConfig(HappConfig{AutoDetect: true}))
		if got := localDirectGet(router, "/sub/s1", happUA).Header().Get("Routing-Enable"); got != "true" {
			t.Fatalf("Routing-Enable = %q, want true", got)
		}
	})
	t.Run("incy gets the profile without the happ switch", func(t *testing.T) {
		router := localDirectRouter(t, WithSUBLocalDirect(localDirectTestPlan(t)))
		resp := localDirectGet(router, "/sub/s1", "INCY/1.0/ios")
		if got := decodeHappRouting(t, resp.Header().Get("Routing")); !reflect.DeepEqual(got, wantProfile) {
			t.Fatalf("profile = %#v", got)
		}
		if got := resp.Header().Get("Routing-Enable"); got != "" {
			t.Fatalf("Routing-Enable = %q, want it unset for INCY", got)
		}
	})
	t.Run("the operator's happ profile wins", func(t *testing.T) {
		router := localDirectRouter(t, WithSUBLocalDirect(localDirectTestPlan(t)), WithSUBRoutingRules("happ://routing/onadd/"+base64.StdEncoding.EncodeToString([]byte(`{"Name":"Mine"}`))))
		resp := localDirectGet(router, "/sub/s1", happUA)
		if got := decodeHappRouting(t, resp.Header().Get("Routing")); !reflect.DeepEqual(got, map[string]any{"Name": "Mine"}) {
			t.Fatalf("profile = %#v, want the operator's", got)
		}
		if got := resp.Header().Get("Routing-Enable"); got != "" {
			t.Fatalf("Routing-Enable = %q, want the operator's own switch left alone", got)
		}
	})
	t.Run("a json profile that cannot be read still keeps it out", func(t *testing.T) {
		router := localDirectRouter(t, WithSUBLocalDirect(localDirectTestPlan(t)), WithSUBJsonRoutingRules("not a profile"))
		resp := localDirectGet(router, "/sub/s1", happUA)
		if got := resp.Header().Get("Routing") + resp.Header().Get("Routing-Enable"); got != "" {
			t.Fatalf("Routing headers = %q, want none: the JSON document carries no local-direct rules either", got)
		}
	})
	t.Run("the operator's json profile wins", func(t *testing.T) {
		router := localDirectRouter(t, WithSUBLocalDirect(localDirectTestPlan(t)), WithSUBJsonRoutingRules(`{"Name":"Baked"}`))
		resp := localDirectGet(router, "/sub/s1", happUA)
		if got := decodeHappRouting(t, resp.Header().Get("Routing")); !reflect.DeepEqual(got, map[string]any{"Name": "Baked"}) {
			t.Fatalf("profile = %#v, want the operator's", got)
		}
		if got := resp.Header().Get("Routing-Enable"); got != "" {
			t.Fatalf("Routing-Enable = %q, want the operator's own switch left alone", got)
		}
	})
}

func TestShadowrocketConfEndpoint(t *testing.T) {
	plan := localDirectTestPlan(t)
	router := localDirectRouter(t, WithSUBLocalDirect(plan))

	resp := localDirectGet(router, "/sub/s1/shadowrocket.conf", "Shadowrocket/2615 CFNetwork/1568.200.51 Darwin/24.1.0")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Body.String(); got != plan.Shadowrocket() || !strings.HasSuffix(got, "GEOIP,RU,DIRECT\nFINAL,PROXY\n") {
		t.Fatalf("body is not the plan's Shadowrocket config:\n%s", got)
	}
	if got := resp.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := resp.Header().Get("Content-Disposition"); got != `attachment; filename="shadowrocket.conf"` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := resp.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	if resp := localDirectGet(router, "/sub/unknown/shadowrocket.conf", ""); resp.Code != http.StatusNotFound || resp.Body.Len() != 0 {
		t.Fatalf("unknown subscription: status = %d, body = %q; want an empty 404", resp.Code, resp.Body.String())
	}
}

func TestLocalDirectSubscriptionPageData(t *testing.T) {
	router := localDirectRouter(t, WithSUBLocalDirect(localDirectTestPlan(t)))
	resp := localDirectGet(router, "/sub/s1?format=info", "")
	var info map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &info); err != nil {
		t.Fatalf("info: %v; body=%s", err, resp.Body.String())
	}
	if info["localDirect"] != "ru" {
		t.Fatalf("localDirect = %v, want ru", info["localDirect"])
	}
	if got, _ := info["shadowrocketConfUrl"].(string); got != info["subUrl"].(string)+"/shadowrocket.conf" || !strings.HasSuffix(got, "/sub/s1/shadowrocket.conf") {
		t.Fatalf("shadowrocketConfUrl = %q, subUrl = %v", got, info["subUrl"])
	}
}

type localDirectResponse struct {
	Status int
	Header http.Header
	Body   string
}

// A panel that upgrades has no local-direct rows, and that must change nothing a client
// receives: every body and header equals what a controller without the feature serves.
func TestLocalDirectOffIsByteIdentical(t *testing.T) {
	oldDistFS := distFS
	distFS = testDistFS
	t.Cleanup(func() { distFS = oldDistFS })

	requests := []struct{ path, userAgent, accept string }{
		{path: "/sub/s1"},
		{path: "/sub/s1", userAgent: "Happ/3.2.1"},
		{path: "/sub/s1", userAgent: "INCY/1.0/ios"},
		{path: "/sub/s1", accept: "text/html"},
		{path: "/sub/s1?format=info"},
		{path: "/json/s1"},
		{path: "/json/s1", userAgent: "Happ/3.2.1"},
		{path: "/clash/s1"},
		{path: "/clash-legacy/s1"},
		{path: "/sub/s1/shadowrocket.conf"},
	}
	fetchAll := func(t *testing.T, localDirect func() []SUBControllerOption) []localDirectResponse {
		t.Helper()
		seedLocalDirectSub(t)
		router := newLocalDirectRouter(append([]SUBControllerOption{WithSUBHappConfig(HappConfig{AutoDetect: true})}, localDirect()...)...)
		out := make([]localDirectResponse, 0, len(requests))
		for _, request := range requests {
			req := httptest.NewRequest(http.MethodGet, "http://sub.example.com"+request.path, nil)
			req.Header.Set("User-Agent", request.userAgent)
			req.Header.Set("Accept", request.accept)
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			out = append(out, localDirectResponse{Status: resp.Code, Header: resp.Header(), Body: resp.Body.String()})
		}
		return out
	}

	var baseline, off, on []localDirectResponse
	t.Run("controller without the option", func(t *testing.T) {
		baseline = fetchAll(t, func() []SUBControllerOption { return nil })
	})
	t.Run("settings never saved", func(t *testing.T) {
		off = fetchAll(t, func() []SUBControllerOption {
			return []SUBControllerOption{WithSUBLocalDirect(loadLocalDirect(&service.SettingService{}))}
		})
	})
	t.Run("feature on", func(t *testing.T) {
		on = fetchAll(t, func() []SUBControllerOption {
			return []SUBControllerOption{WithSUBLocalDirect(localDirectTestPlan(t))}
		})
	})
	if len(baseline) != len(requests) || len(off) != len(requests) || len(on) != len(requests) {
		t.Fatalf("fetched %d, %d and %d responses, want %d each", len(baseline), len(off), len(on), len(requests))
	}
	for i, request := range requests {
		if baseline[i].Status != http.StatusOK && request.path != "/sub/s1/shadowrocket.conf" {
			t.Errorf("%s (UA %q): baseline status = %d, want 200", request.path, request.userAgent, baseline[i].Status)
		}
		if !reflect.DeepEqual(off[i], baseline[i]) {
			t.Errorf("%s (UA %q): response differs with the feature off\n got %+v\nwant %+v", request.path, request.userAgent, off[i], baseline[i])
		}
		if reflect.DeepEqual(on[i], baseline[i]) {
			t.Errorf("%s (UA %q): the comparison cannot tell on from off", request.path, request.userAgent)
		}
	}
	if last := baseline[len(baseline)-1]; last.Status != http.StatusNotFound {
		t.Errorf("shadowrocket.conf without the feature: status = %d, want 404", last.Status)
	}
}

func writeGeoFile(t *testing.T, dir, name string, message proto.Message) {
	t.Helper()
	data, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func saveLocalDirectCountry(t *testing.T, country string) {
	t.Helper()
	db := database.GetDB()
	if err := db.Where("key = ?", "subLocalDirect").Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Setting{Key: "subLocalDirect", Value: country}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestLoadLocalDirectReadsThePanelGeoDatabases(t *testing.T) {
	seedSubDB(t)
	assets := t.TempDir()
	t.Setenv("XRAY_LOCATION_ASSET", assets)
	writeGeoFile(t, assets, localdirect.GeositeFile, &xraygeodata.GeoSiteList{Entry: []*xraygeodata.GeoSite{
		{Code: "CATEGORY-RU", Domain: []*xraygeodata.Domain{{Type: xraygeodata.Domain_Domain, Value: "yandex.net"}}},
	}})
	writeGeoFile(t, assets, localdirect.GeoIPFile, &xraygeodata.GeoIPList{Entry: []*xraygeodata.GeoIP{
		{Code: "RU", Cidr: []*xraygeodata.CIDR{{Ip: []byte{5, 255, 255, 0}, Prefix: 24}}},
	}})
	settings := &service.SettingService{}

	tests := []struct {
		country     string
		wantCountry string
		wantGeosite string
		wantGeoIP   bool
	}{
		{country: "ru", wantCountry: "ru", wantGeosite: "category-ru", wantGeoIP: true},
		{country: "ir", wantCountry: "ir"},
		{country: "xx"},
		{country: ""},
	}
	for _, tt := range tests {
		t.Run("country "+tt.country, func(t *testing.T) {
			saveLocalDirectCountry(t, tt.country)
			plan := loadLocalDirect(settings)
			if plan.Country != tt.wantCountry || plan.Geosite != tt.wantGeosite || plan.GeoIP != tt.wantGeoIP {
				t.Fatalf("plan = %+v, want country %q, geosite %q, geoip %v", plan, tt.wantCountry, tt.wantGeosite, tt.wantGeoIP)
			}
			if tt.wantCountry != "" && !slices.Contains(plan.ProxyIPs, "91.108.0.0/16") {
				t.Fatalf("the built-in always-VPN list did not load: %v", plan.ProxyIPs)
			}
		})
	}
}
