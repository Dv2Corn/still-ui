package localdirect

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const testProxyList = "# messengers\ntelegram.org\n91.108.0.0/16\n2001:67c:4e8::/48\n"

func allGeo(string, string) bool { return true }

func noGeo(string, string) bool { return false }

func mustPlan(t *testing.T, country, proxyList string, has func(file, code string) bool) Plan {
	t.Helper()
	plan, err := NewPlan(country, proxyList, has)
	if err != nil {
		t.Fatalf("NewPlan(%q): %v", country, err)
	}
	return plan
}

func TestNormalizeCountry(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr string
	}{
		{raw: "", want: ""},
		{raw: " off ", want: ""},
		{raw: "RU", want: "ru"},
		{raw: " ir\n", want: "ir"},
		{raw: "zz", wantErr: `unknown country code "zz"`},
		{raw: "rus", wantErr: `unknown country code "rus"`},
		{raw: "su", wantErr: `unknown country code "su"`},
		{raw: "r u", wantErr: `unknown country code "r u"`},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := NormalizeCountry(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("NormalizeCountry(%q) = %q, %v; want %q", tt.raw, got, err, tt.want)
			}
		})
	}
}

func TestNormalizeProxyList(t *testing.T) {
	t.Run("blank falls back to the built-in list", func(t *testing.T) {
		got, err := NormalizeProxyList(" \r\n ")
		if err != nil || got != DefaultProxyList {
			t.Fatalf("got %q, %v; want the built-in list", got, err)
		}
	})
	t.Run("keeps comments and drops carriage returns", func(t *testing.T) {
		got, err := NormalizeProxyList("# mine\r\nExample.COM\r\n10.1.2.3\r\n")
		if err != nil || got != "# mine\nExample.COM\n10.1.2.3" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	for _, bad := range []string{
		"telegram.org,PROXY",
		"exa mple.com",
		"geosite:telegram",
		"пример.рф",
		"-bad.example",
		"91.108.0.0/33",
		"https://telegram.org",
	} {
		t.Run("rejects "+bad, func(t *testing.T) {
			_, err := NormalizeProxyList("telegram.org\n" + bad)
			if err == nil || !strings.Contains(err.Error(), "line 2") {
				t.Fatalf("err = %v, want a line 2 error", err)
			}
		})
	}
	t.Run("rejects an oversized list", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i <= MaxProxyEntries; i++ {
			b.WriteString("d" + strconv.Itoa(i) + ".example\n")
		}
		if _, err := NormalizeProxyList(b.String()); err == nil || !strings.Contains(err.Error(), "at most") {
			t.Fatalf("err = %v, want the entry cap error", err)
		}
	})
}

func TestNewPlanRejectsUnknownCountry(t *testing.T) {
	plan, err := NewPlan("xx", testProxyList, allGeo)
	if err == nil || plan.Enabled() {
		t.Fatalf("plan = %+v, err = %v; want an error and a disabled plan", plan, err)
	}
}

func TestNewPlanOff(t *testing.T) {
	for _, country := range []string{"", "off"} {
		plan := mustPlan(t, country, testProxyList, allGeo)
		if plan.Enabled() || plan.XrayRules() != nil || plan.ClashRules(true) != nil || plan.Shadowrocket() != "" || plan.HappProfile() != nil {
			t.Fatalf("country %q: off plan still renders rules: %+v", country, plan)
		}
	}
}

func TestNewPlanCountryTable(t *testing.T) {
	tests := []struct {
		country     string
		geosite     map[string]bool
		wantTLDs    []string
		wantGeosite string
	}{
		{country: "ru", geosite: map[string]bool{"category-ru": true}, wantTLDs: []string{"ru", "su", "xn--p1ai"}, wantGeosite: "category-ru"},
		{country: "by", wantTLDs: []string{"by", "xn--90ais"}},
		{country: "kz", wantTLDs: []string{"kz", "xn--80ao21a"}},
		{country: "ir", geosite: map[string]bool{"category-ir": true}, wantTLDs: []string{"ir"}, wantGeosite: "category-ir"},
		{country: "tr", wantTLDs: []string{"tr"}},
		{country: "cn", geosite: map[string]bool{"geolocation-cn": true}, wantTLDs: []string{"cn"}, wantGeosite: "geolocation-cn"},
		{country: "cn", geosite: map[string]bool{"geolocation-cn": true, "cn": true}, wantTLDs: []string{"cn"}, wantGeosite: "cn"},
		{country: "gb", wantTLDs: []string{"uk"}},
		{country: "de", geosite: map[string]bool{"de": true}, wantTLDs: []string{"de"}},
	}
	for _, tt := range tests {
		t.Run(tt.country, func(t *testing.T) {
			plan := mustPlan(t, tt.country, testProxyList, func(file, code string) bool {
				return file == GeositeFile && tt.geosite[code]
			})
			if !slices.Equal(plan.TLDs, tt.wantTLDs) || plan.Geosite != tt.wantGeosite || plan.GeoIP {
				t.Fatalf("plan = %+v, want TLDs %v, geosite %q and no geoip", plan, tt.wantTLDs, tt.wantGeosite)
			}
		})
	}
}

func TestNewPlanAsksGeoIPForTheCountry(t *testing.T) {
	plan := mustPlan(t, "ru", testProxyList, func(file, code string) bool {
		return file == GeoIPFile && code == "ru"
	})
	if !plan.GeoIP || plan.Geosite != "" {
		t.Fatalf("plan = %+v, want geoip without geosite", plan)
	}
}

func TestXrayRulesGolden(t *testing.T) {
	got, err := json.Marshal(mustPlan(t, "ru", testProxyList, allGeo).XrayRules())
	if err != nil {
		t.Fatal(err)
	}
	want := `[` +
		`{"ip":["10.0.0.0/8","100.64.0.0/10","127.0.0.0/8","169.254.0.0/16","172.16.0.0/12","192.168.0.0/16","::1/128","fc00::/7","fe80::/10"],"outboundTag":"direct","type":"field"},` +
		`{"domain":["domain:telegram.org"],"outboundTag":"proxy","type":"field"},` +
		`{"ip":["91.108.0.0/16","2001:67c:4e8::/48"],"outboundTag":"proxy","type":"field"},` +
		`{"domain":["domain:ru","domain:su","domain:xn--p1ai","geosite:category-ru"],"outboundTag":"direct","type":"field"},` +
		`{"ip":["geoip:ru"],"outboundTag":"direct","type":"field"}` +
		`]`
	if string(got) != want {
		t.Fatalf("xray rules:\n got %s\nwant %s", got, want)
	}
}

func TestXrayRulesWithoutGeodataKeepOnlyTLDs(t *testing.T) {
	got, err := json.Marshal(mustPlan(t, "ru", "telegram.org", noGeo).XrayRules())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "geosite:") || strings.Contains(string(got), "geoip:") {
		t.Fatalf("rules name a geo category the databases do not carry: %s", got)
	}
	if !strings.Contains(string(got), `{"domain":["domain:ru","domain:su","domain:xn--p1ai"],"outboundTag":"direct","type":"field"}`) {
		t.Fatalf("TLD rule missing: %s", got)
	}
}

func TestClashRulesGolden(t *testing.T) {
	plan := mustPlan(t, "ru", testProxyList, allGeo)
	want := []string{
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
		"IP-CIDR6,2001:67c:4e8::/48,PROXY,no-resolve",
		"DOMAIN-SUFFIX,ru,DIRECT",
		"DOMAIN-SUFFIX,su,DIRECT",
		"DOMAIN-SUFFIX,xn--p1ai,DIRECT",
		"GEOSITE,category-ru,DIRECT",
		"GEOIP,RU,DIRECT",
	}
	if got := plan.ClashRules(true); !reflect.DeepEqual(got, want) {
		t.Fatalf("clash rules:\n got %q\nwant %q", got, want)
	}
	withoutGeosite := slices.DeleteFunc(slices.Clone(want), func(rule string) bool { return strings.HasPrefix(rule, "GEOSITE,") })
	if got := plan.ClashRules(false); !reflect.DeepEqual(got, withoutGeosite) {
		t.Fatalf("clash rules without GEOSITE:\n got %q\nwant %q", got, withoutGeosite)
	}
}

func TestShadowrocketGolden(t *testing.T) {
	got := mustPlan(t, "ru", testProxyList, allGeo).Shadowrocket()
	want := `# Local sites direct (RU): sites of this country open directly, everything else goes through the VPN.
# Import it in Shadowrocket (Config tab) and switch Global Routing to "Config".
# The servers come from your subscription; this file has no [Proxy] section.
[General]
bypass-system = true
skip-proxy = 192.168.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 100.64.0.0/10, localhost, *.local, captive.apple.com
tun-excluded-routes = 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/4, 255.255.255.255/32
dns-server = https://dns.google/dns-query, https://unfiltered.adguard-dns.com/dns-query
fallback-dns-server = system
dns-direct-system = true
dns-direct-fallback-proxy = true
ipv6 = true
prefer-ipv6 = false
icmp-auto-reply = true
private-ip-answer = true

[Rule]
IP-CIDR,10.0.0.0/8,DIRECT,no-resolve
IP-CIDR,100.64.0.0/10,DIRECT,no-resolve
IP-CIDR,127.0.0.0/8,DIRECT,no-resolve
IP-CIDR,169.254.0.0/16,DIRECT,no-resolve
IP-CIDR,172.16.0.0/12,DIRECT,no-resolve
IP-CIDR,192.168.0.0/16,DIRECT,no-resolve
IP-CIDR6,::1/128,DIRECT,no-resolve
IP-CIDR6,fc00::/7,DIRECT,no-resolve
IP-CIDR6,fe80::/10,DIRECT,no-resolve
DOMAIN-SUFFIX,telegram.org,PROXY
IP-CIDR,91.108.0.0/16,PROXY,no-resolve
IP-CIDR6,2001:67c:4e8::/48,PROXY,no-resolve
DOMAIN-SUFFIX,ru,DIRECT
DOMAIN-SUFFIX,su,DIRECT
DOMAIN-SUFFIX,xn--p1ai,DIRECT
GEOIP,RU,DIRECT
FINAL,PROXY
`
	if got != want {
		t.Fatalf("shadowrocket conf:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestHappProfileGolden(t *testing.T) {
	got := string(mustPlan(t, "ru", testProxyList, allGeo).HappProfile())
	want := `{"Name":"Local direct RU","GlobalProxy":"true","RouteOrder":"block-proxy-direct","DomainStrategy":"IPIfNonMatch",` +
		`"DirectSites":["domain:ru","domain:su","domain:xn--p1ai","geosite:category-ru"],` +
		`"DirectIp":["10.0.0.0/8","100.64.0.0/10","127.0.0.0/8","169.254.0.0/16","172.16.0.0/12","192.168.0.0/16","::1/128","fc00::/7","fe80::/10","geoip:ru"],` +
		`"ProxySites":["domain:telegram.org"],"ProxyIp":["91.108.0.0/16","2001:67c:4e8::/48"],"BlockSites":[],"BlockIp":[]}`
	if got != want {
		t.Fatalf("happ profile:\n got %s\nwant %s", got, want)
	}
}

// Telegram's ranges are registered as Russian in the geoip databases, so the
// built-in list has to reach the VPN before the country rule in every format.
func TestDefaultListKeepsTelegramOffTheDirectRoute(t *testing.T) {
	plan := mustPlan(t, "ru", DefaultProxyList, allGeo)
	telegram := []string{
		"91.108.0.0/16", "149.154.160.0/20", "95.161.64.0/20", "185.76.151.0/24",
		"2001:67c:4e8::/48", "2001:b28:f23c::/46", "2a0a:f280::/32",
	}

	t.Run("xray", func(t *testing.T) {
		rules := plan.XrayRules()
		proxyAt, countryAt := -1, -1
		for i, rule := range rules {
			fields := rule.(map[string]any)
			ips, _ := fields["ip"].([]string)
			if fields["outboundTag"] == "proxy" && slices.Contains(ips, telegram[0]) {
				proxyAt = i
				for _, cidr := range telegram {
					if !slices.Contains(ips, cidr) {
						t.Fatalf("always-VPN rule misses %s: %v", cidr, ips)
					}
				}
			}
			if fields["outboundTag"] == "direct" {
				for _, cidr := range telegram {
					if slices.Contains(ips, cidr) {
						t.Fatalf("%s is routed direct: %v", cidr, ips)
					}
				}
				if slices.Contains(ips, "geoip:ru") {
					countryAt = i
				}
			}
		}
		if proxyAt < 0 || countryAt < 0 || proxyAt > countryAt {
			t.Fatalf("always-VPN rule at %d, geoip:ru at %d; the VPN rule must come first", proxyAt, countryAt)
		}
	})

	for name, rules := range map[string][]string{
		"clash":        plan.ClashRules(true),
		"shadowrocket": strings.Split(plan.Shadowrocket(), "\n"),
	} {
		t.Run(name, func(t *testing.T) {
			countryAt := slices.Index(rules, "GEOIP,RU,DIRECT")
			tldAt := slices.Index(rules, "DOMAIN-SUFFIX,ru,DIRECT")
			domainAt := slices.Index(rules, "DOMAIN-SUFFIX,telegram.org,PROXY")
			if countryAt < 0 || tldAt < 0 || domainAt < 0 || domainAt > tldAt {
				t.Fatalf("telegram.org at %d, .ru at %d, GEOIP at %d", domainAt, tldAt, countryAt)
			}
			for _, cidr := range telegram {
				kind := "IP-CIDR"
				if strings.Contains(cidr, ":") {
					kind = "IP-CIDR6"
				}
				at := slices.Index(rules, kind+","+cidr+",PROXY,no-resolve")
				if at < 0 || at > countryAt {
					t.Fatalf("%s at %d, GEOIP,RU at %d; the VPN rule must come first", cidr, at, countryAt)
				}
			}
		})
	}

	t.Run("happ", func(t *testing.T) {
		var profile struct {
			RouteOrder string
			DirectIp   []string
			ProxyIp    []string
		}
		if err := json.Unmarshal(plan.HappProfile(), &profile); err != nil {
			t.Fatal(err)
		}
		if profile.RouteOrder != "block-proxy-direct" {
			t.Fatalf("RouteOrder = %q, want proxy ahead of direct", profile.RouteOrder)
		}
		for _, cidr := range telegram {
			if !slices.Contains(profile.ProxyIp, cidr) || slices.Contains(profile.DirectIp, cidr) {
				t.Fatalf("%s: ProxyIp %v, DirectIp %v", cidr, profile.ProxyIp, profile.DirectIp)
			}
		}
	})
}

func TestDefaultListCoversTheNamedServices(t *testing.T) {
	plan := mustPlan(t, "ru", DefaultProxyList, allGeo)
	for _, domain := range []string{
		"telegram.org", "t.me", "youtube.com", "googlevideo.com", "instagram.com", "facebook.com",
		"whatsapp.com", "whatsapp.net", "discord.com", "openai.com", "chatgpt.com", "anthropic.com", "claude.ai",
	} {
		if !slices.Contains(plan.ProxyDomains, domain) {
			t.Errorf("built-in always-VPN list misses %s", domain)
		}
	}
}

func TestShadowrocketIsASCII(t *testing.T) {
	conf := mustPlan(t, "ir", DefaultProxyList, allGeo).Shadowrocket()
	for i := 0; i < len(conf); i++ {
		if conf[i] > 0x7e || (conf[i] < 0x20 && conf[i] != '\n') {
			t.Fatalf("byte %#x at offset %d is not printable ASCII", conf[i], i)
		}
	}
}

func TestProxyListEntriesAreCanonical(t *testing.T) {
	plan := mustPlan(t, "ru", "Example.COM\nexample.com\n10.1.2.3\n10.1.2.3/32\n2001:DB8::1/32\n", allGeo)
	if !slices.Equal(plan.ProxyDomains, []string{"example.com"}) {
		t.Fatalf("domains = %v", plan.ProxyDomains)
	}
	if !slices.Equal(plan.ProxyIPs, []string{"10.1.2.3/32", "2001:db8::/32"}) {
		t.Fatalf("ips = %v", plan.ProxyIPs)
	}
}
