package sub

import (
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// clashAutoGroupName names the group clients use to pick a working proxy on
// their own. Custom Clash rules and remote templates may reference it.
const clashAutoGroupName = "Auto"

// ClashAutoGroup configures the Clash/Mihomo "Auto" group: when Enabled it tests every proxy and is
// the first, default choice of PROXY, where manual choice stays.
type ClashAutoGroup struct {
	Enabled   bool
	Type      string
	URL       string
	Interval  int
	Tolerance int
}

func defaultClashAutoGroup() ClashAutoGroup {
	return ClashAutoGroup{
		Type:      service.DefaultSubClashAutoGroupType,
		URL:       service.DefaultSubClashAutoGroupURL,
		Interval:  service.DefaultSubClashAutoGroupInterval,
		Tolerance: service.DefaultSubClashAutoGroupTolerance,
	}
}

// normalized replaces values a client would reject with the defaults, so a hand-edited row cannot
// break every Clash subscription; NewSUBController applies it once.
func (g ClashAutoGroup) normalized() ClashAutoGroup {
	def := defaultClashAutoGroup()
	g.Type = strings.TrimSpace(g.Type)
	if g.Type != service.SubClashAutoGroupURLTest && g.Type != service.SubClashAutoGroupFallback {
		g.Type = def.Type
	}
	g.URL = strings.TrimSpace(g.URL)
	if !service.ValidSubClashAutoGroupURL(g.URL) {
		g.URL = def.URL
	}
	if g.Interval < 10 || g.Interval > 86400 {
		g.Interval = def.Interval
	}
	if g.Tolerance < 0 || g.Tolerance > 10000 {
		g.Tolerance = def.Tolerance
	}
	return g
}

func (g ClashAutoGroup) proxyGroup(members []string) map[string]any {
	group := map[string]any{
		"name":     clashAutoGroupName,
		"type":     g.Type,
		"proxies":  members,
		"url":      g.URL,
		"interval": g.Interval,
		"lazy":     true,
	}
	if g.Type == service.SubClashAutoGroupURLTest {
		// Without a tolerance url-test hops on every few ms of jitter.
		group["tolerance"] = g.Tolerance
	}
	return group
}

func loadClashAutoGroup(settings *service.SettingService) ClashAutoGroup {
	g := defaultClashAutoGroup()
	if v, err := settings.GetSubClashAutoGroup(); err == nil {
		g.Enabled = v
	}
	if v, err := settings.GetSubClashAutoGroupType(); err == nil {
		g.Type = v
	}
	if v, err := settings.GetSubClashAutoGroupUrl(); err == nil {
		g.URL = v
	}
	if v, err := settings.GetSubClashAutoGroupInterval(); err == nil {
		g.Interval = v
	}
	if v, err := settings.GetSubClashAutoGroupTolerance(); err == nil {
		g.Tolerance = v
	}
	return g
}

// clashProxyGroups builds PROXY (names, then DIRECT) and Auto (members): Auto leads PROXY when on with
// two or more proxies, and is emitted outside PROXY whenever the custom rules route to it.
func (g ClashAutoGroup) clashProxyGroups(names, members []string, templateMentionsAuto bool) []map[string]any {
	show := g.Enabled && len(members) >= 2
	selectable := make([]string, 0, len(names)+2)
	if show {
		selectable = append(selectable, clashAutoGroupName)
	}
	selectable = append(selectable, names...)
	selectable = append(selectable, "DIRECT")
	groups := []map[string]any{{
		"name":    "PROXY",
		"type":    "select",
		"proxies": selectable,
	}}
	if len(members) == 0 {
		members = names
	}
	if (show || templateMentionsAuto) && len(members) > 0 {
		groups = append(groups, g.proxyGroup(members))
	}
	return groups
}

// clashAutoGroupMayAppear reports whether the Auto group can be part of the
// output, so no proxy may take its name.
func (g ClashAutoGroup) clashAutoGroupMayAppear(templateMentionsAuto bool) bool {
	return g.Enabled || templateMentionsAuto
}

func clashTemplateMentionsAuto(template string) bool {
	return strings.Contains(template, clashAutoGroupName)
}
