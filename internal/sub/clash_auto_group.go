package sub

import (
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// clashAutoGroupName names the group clients use to pick a working proxy on
// their own. Custom Clash rules and remote templates may reference it.
const clashAutoGroupName = "Auto"

// ClashAutoGroup configures the Clash/Mihomo "Auto" group. With Enabled the
// group lists every proxy and is the first, default choice of the PROXY
// select group; manual choice stays available there.
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

// normalized replaces values a client would reject with the defaults, so a
// hand-edited database row cannot break every Clash subscription.
// NewSUBController applies it once to whatever the settings hold.
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

// clashProxyGroups builds the PROXY select group and, when wanted, the Auto
// group. names are the selectable proxies (without DIRECT); members are the
// real proxies the Auto group may test.
//
// The Auto group is shown — first in PROXY, so it is the default — only when
// enabled and there is a choice to make (two or more proxies). It is still
// emitted, outside PROXY, when the custom rules mention it, so a template that
// routes to Auto keeps working for a single-proxy or Auto-off subscription.
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
