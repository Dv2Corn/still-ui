// Package localdirect builds the "local sites direct" subscription routing: one country's
// sites bypass the VPN, a short always-VPN list wins over that, everything else is proxied.
package localdirect

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

const (
	GeositeFile = "geosite.dat"
	GeoIPFile   = "geoip.dat"

	// What the always-VPN list may take inside Happ's routing profile; with the fixed
	// part of the profile, base64 of this stays under the sub server's 8 KiB Routing header.
	MaxProxyBytes = 5000
)

// BuiltinProxyList always goes through the VPN. Telegram's ranges are registered as
// Russian in geoip databases, and Google serves video from caches inside local ISPs.
const BuiltinProxyList = `# Telegram
telegram.org
t.me
telegram.me
telegra.ph
telesco.pe
cdn-telegram.org
tdesktop.com
91.108.0.0/16
149.154.160.0/20
95.161.64.0/20
185.76.151.0/24
2001:67c:4e8::/48
2001:b28:f23c::/46
2a0a:f280::/32
# YouTube
youtube.com
youtu.be
googlevideo.com
ytimg.com
ggpht.com
youtube-nocookie.com
# Instagram, Facebook
instagram.com
cdninstagram.com
facebook.com
fbcdn.net
fb.com
messenger.com
threads.net
# WhatsApp
whatsapp.com
whatsapp.net
wa.me
# Discord
discord.com
discord.gg
discord.media
discordapp.com
discordapp.net
# OpenAI
openai.com
chatgpt.com
oaistatic.com
oaiusercontent.com
# Anthropic
anthropic.com
claude.ai
claude.com`

var privateCIDRs = []string{
	"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16",
	"::1/128", "fc00::/7", "fe80::/10",
}

var domainRegex = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Plan is the resolved routing for one country; the zero value means the feature is off.
type Plan struct {
	Country      string
	ProxyDomains []string
	ProxyIPs     []string
	TLDs         []string
	// Geosite and GeoIP are set only when the panel's own databases carry the category:
	// a client core refuses a whole profile that names one it cannot find.
	Geosite string
	GeoIP   bool
}

// NormalizeCountry maps the setting to "" (off) or a lowercase ISO 3166-1 alpha-2 code.
func NormalizeCountry(raw string) (string, error) {
	code := strings.ToLower(strings.TrimSpace(raw))
	if code == "" || code == "off" {
		return "", nil
	}
	if _, ok := knownCountries[code]; !ok {
		return "", fmt.Errorf("unknown country code %q: use an ISO 3166-1 alpha-2 code such as ru or ir", strings.TrimSpace(raw))
	}
	return code, nil
}

// NormalizeProxyList validates the operator's additions to the built-in always-VPN
// list and returns the text to store.
func NormalizeProxyList(raw string) (string, error) {
	text := strings.TrimSpace(strings.ReplaceAll(raw, "\r", ""))
	if _, _, err := proxyList(text); err != nil {
		return "", err
	}
	return text, nil
}

// proxyList returns the built-in entries followed by the operator's own.
func proxyList(extra string) (domains, ips []string, err error) {
	domains, ips, err = parseProxyList(BuiltinProxyList, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return parseProxyList(extra, domains, ips)
}

func parseProxyList(raw string, domains, ips []string) ([]string, []string, error) {
	for i, line := range strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n") {
		entry := strings.ToLower(strings.TrimSpace(line))
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		if prefix, ok := parsePrefix(entry); ok {
			if !slices.Contains(ips, prefix) {
				ips = append(ips, prefix)
			}
		} else if len(entry) <= 253 && domainRegex.MatchString(entry) {
			if !slices.Contains(domains, entry) {
				domains = append(domains, entry)
			}
		} else {
			return nil, nil, fmt.Errorf("always-VPN list line %d: %q is neither a domain nor an IP range", i+1, strings.TrimSpace(line))
		}
	}
	// A profile entry is "domain:<name>", or "<range>", with its quotes and comma.
	size := 10*len(domains) + 3*len(ips)
	for _, entry := range slices.Concat(domains, ips) {
		size += len(entry)
	}
	if size > MaxProxyBytes {
		return nil, nil, fmt.Errorf("always-VPN list is too long for Happ's routing header: %d of %d bytes", size, MaxProxyBytes)
	}
	return domains, ips, nil
}

func parsePrefix(entry string) (string, bool) {
	if prefix, err := netip.ParsePrefix(entry); err == nil {
		return prefix.Masked().String(), true
	}
	if addr, err := netip.ParseAddr(entry); err == nil && addr.Zone() == "" {
		return netip.PrefixFrom(addr, addr.BitLen()).String(), true
	}
	return "", false
}

// NewPlan resolves the two settings into a plan; has reports whether a geo database
// on this panel carries a category. An error means the stored settings are unusable.
func NewPlan(country, extraProxyList string, has func(file, code string) bool) (Plan, error) {
	code, err := NormalizeCountry(country)
	if err != nil || code == "" {
		return Plan{}, err
	}
	domains, ips, err := proxyList(extraProxyList)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Country: code, ProxyDomains: domains, ProxyIPs: ips, TLDs: countryTLDs[code]}
	if plan.TLDs == nil {
		plan.TLDs = []string{code}
	}
	candidates := countryGeosites[code]
	if candidates == nil {
		candidates = []string{"category-" + code}
	}
	for _, candidate := range candidates {
		if has(GeositeFile, candidate) {
			plan.Geosite = candidate
			break
		}
	}
	plan.GeoIP = has(GeoIPFile, code)
	return plan, nil
}

func (p Plan) Enabled() bool {
	return p.Country != ""
}

func prefixed(prefix string, values []string) []string {
	out := make([]string, 0, len(values)+1)
	for _, value := range values {
		out = append(out, prefix+value)
	}
	return out
}

func (p Plan) directSites() []string {
	sites := prefixed("domain:", p.TLDs)
	if p.Geosite != "" {
		sites = append(sites, "geosite:"+p.Geosite)
	}
	return sites
}

// XrayRules returns the routing rules for domainStrategy IPOnDemand, ahead of the caller's
// catch-all. Domain rules lead so that only a name none of them knows costs a DNS lookup.
func (p Plan) XrayRules() []any {
	if !p.Enabled() {
		return nil
	}
	rule := func(key string, values []string, outbound string) map[string]any {
		return map[string]any{"type": "field", key: values, "outboundTag": outbound}
	}
	var rules []any
	if len(p.ProxyDomains) > 0 {
		rules = append(rules, rule("domain", prefixed("domain:", p.ProxyDomains), "proxy"))
	}
	rules = append(rules, rule("domain", p.directSites(), "direct"), rule("ip", privateCIDRs, "direct"))
	if len(p.ProxyIPs) > 0 {
		rules = append(rules, rule("ip", p.ProxyIPs, "proxy"))
	}
	if p.GeoIP {
		rules = append(rules, rule("ip", []string{"geoip:" + p.Country}, "direct"))
	}
	return rules
}

func cidrRule(cidr, target string) string {
	kind := "IP-CIDR"
	if strings.Contains(cidr, ":") {
		kind = "IP-CIDR6"
	}
	return kind + "," + cidr + "," + target + ",no-resolve"
}

// ClashRules returns Clash-syntax rules routing to the PROXY group, without the final
// MATCH. Clash for Windows and Shadowrocket have no GEOSITE rule, hence the switch.
func (p Plan) ClashRules(geosite bool) []string {
	if !p.Enabled() {
		return nil
	}
	rules := make([]string, 0, len(privateCIDRs)+len(p.ProxyDomains)+len(p.ProxyIPs)+len(p.TLDs)+2)
	for _, cidr := range privateCIDRs {
		rules = append(rules, cidrRule(cidr, "DIRECT"))
	}
	for _, domain := range p.ProxyDomains {
		rules = append(rules, "DOMAIN-SUFFIX,"+domain+",PROXY")
	}
	for _, cidr := range p.ProxyIPs {
		rules = append(rules, cidrRule(cidr, "PROXY"))
	}
	for _, tld := range p.TLDs {
		rules = append(rules, "DOMAIN-SUFFIX,"+tld+",DIRECT")
	}
	if geosite && p.Geosite != "" {
		rules = append(rules, "GEOSITE,"+p.Geosite+",DIRECT")
	}
	// Mihomo and Shadowrocket ship their own country database, so GEOIP needs no check here.
	return append(rules, "GEOIP,"+strings.ToUpper(p.Country)+",DIRECT")
}

const shadowrocketGeneral = `[General]
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
`

// Shadowrocket returns a rules-only config file (ASCII; the servers stay in the subscription).
func (p Plan) Shadowrocket() string {
	if !p.Enabled() {
		return ""
	}
	var conf strings.Builder
	conf.WriteString("# Local sites direct (" + strings.ToUpper(p.Country) + "): sites of this country open directly, everything else goes through the VPN.\n")
	conf.WriteString("# Import it in Shadowrocket (Config tab) and switch Global Routing to \"Config\".\n")
	conf.WriteString("# The servers come from your subscription; this file has no [Proxy] section.\n")
	conf.WriteString(shadowrocketGeneral)
	conf.WriteString("\n[Rule]\n")
	for _, rule := range p.ClashRules(false) {
		conf.WriteString(rule + "\n")
	}
	conf.WriteString("FINAL,PROXY\n")
	return conf.String()
}

// HappProfile returns the routing profile JSON that Happ and INCY take in a Routing header.
func (p Plan) HappProfile() []byte {
	if !p.Enabled() {
		return nil
	}
	directIPs := slices.Clone(privateCIDRs)
	if p.GeoIP {
		directIPs = append(directIPs, "geoip:"+p.Country)
	}
	profile, _ := json.Marshal(struct {
		Name           string
		GlobalProxy    string
		RouteOrder     string
		DomainStrategy string
		DirectSites    []string
		DirectIp       []string
		ProxySites     []string
		ProxyIp        []string
		BlockSites     []string
		BlockIp        []string
	}{
		Name:           "Local direct " + strings.ToUpper(p.Country),
		GlobalProxy:    "true",
		RouteOrder:     "block-proxy-direct",
		DomainStrategy: "IPIfNonMatch",
		DirectSites:    p.directSites(),
		DirectIp:       directIPs,
		ProxySites:     prefixed("domain:", p.ProxyDomains),
		ProxyIp:        append([]string{}, p.ProxyIPs...),
		BlockSites:     []string{},
		BlockIp:        []string{},
	})
	return profile
}
