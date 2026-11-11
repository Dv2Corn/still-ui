package sub

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/sub/localdirect"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const shadowrocketConfName = "shadowrocket.conf"

func WithSUBLocalDirect(value localdirect.Plan) SUBControllerOption {
	return func(config *subControllerConfig) { config.localDirect = value }
}

// loadLocalDirect degrades to off on an unusable stored value: a bad setting
// must never take the subscription server down.
func loadLocalDirect(settings *service.SettingService) localdirect.Plan {
	country, err := settings.GetSubLocalDirect()
	if err != nil || strings.TrimSpace(country) == "" {
		return localdirect.Plan{}
	}
	proxyList, err := settings.GetSubLocalDirectProxy()
	if err != nil {
		logger.Warning("subLocalDirect: could not read the always-VPN list:", err)
		return localdirect.Plan{}
	}
	// shortcut: geo categories are read once per sub-server start; a database swapped
	// later is seen after a panel restart. Re-resolve per request if that ever bites.
	plan, err := localdirect.NewPlan(country, proxyList, (&service.GeodataService{}).HasCategory)
	if err != nil {
		logger.Warning("subLocalDirect:", err)
		return localdirect.Plan{}
	}
	return plan
}

// localDirectHappRouting wraps the plan as the deeplink Happ and INCY take in a
// Routing header; "" when the feature is off or the profile outgrows the header.
func localDirectHappRouting(plan localdirect.Plan) string {
	if !plan.Enabled() {
		return ""
	}
	deeplink, err := normalizeHappRouting(plan.HappProfile())
	if err != nil || len(deeplink) > remoteRoutingHappMaxValue {
		logger.Warning("subLocalDirect: the always-VPN list is too long for a Happ Routing header; Happ gets no routing profile")
		return ""
	}
	return deeplink
}

// SetLocalDirect bakes the plan's rules ahead of the template's catch-all proxy rule.
// An explicit JSON routing profile replaces the routing subtree outright, so it wins.
func (s *SubJsonService) SetLocalDirect(plan localdirect.Plan) {
	routing, _ := s.configJson["routing"].(map[string]any)
	rules, _ := routing["rules"].([]any)
	if !plan.Enabled() || strings.TrimSpace(s.routingRules) != "" || len(rules) == 0 {
		return
	}
	catchAll := len(rules) - 1
	merged := append(slices.Clone(rules[:catchAll]), plan.XrayRules()...)
	routing["rules"] = append(merged, rules[catchAll])
	// IPIfNonMatch resolves a name only when no rule matched, which the catch-all rules out.
	routing["domainStrategy"] = "IPOnDemand"
}

// shadowrocketConf serves the rules-only Shadowrocket config. It carries no credentials, so like
// hwid-status it takes no device slot; an unknown subscription still gets the usual 404.
func (a *SUBController) shadowrocketConf(c *gin.Context) {
	_, found, err := a.clientService.HwidSlotStatusForSubID(c.Param("subid"))
	if err != nil || !found {
		writeSubError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+shadowrocketConfName+`"`)
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(a.localDirect.Shadowrocket()))
}
