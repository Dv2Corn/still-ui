package sub

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The device limit and local-direct rules on together: the registered device gets its server
// with the rules, the device over the limit gets the notice alone on every body route.
func TestDeviceLimitGatesEveryFormatWithLocalDirectOn(t *testing.T) {
	_, subID := initHwidSubRouter(t, 1)
	plan := localDirectTestPlan(t)
	router := gin.New()
	NewSUBController(
		router.Group("/"),
		WithSUBPath("/sub/"),
		WithSUBJsonPath("/json/"),
		WithSUBClashPath("/clash/"),
		WithSUBClashAutoDetect(true),
		WithSUBJsonAutoDetect(true),
		WithSUBJsonEnabled(true),
		WithSUBClashEnabled(true),
		WithSUBLocalDirect(plan),
	)
	routes := hwidBodyRoutes(subID)
	localRule := func(path string) string {
		switch {
		case strings.HasPrefix(path, "/json/"):
			return `"geoip:ru"`
		case strings.HasPrefix(path, "/clash/"):
			return "GEOIP,RU,DIRECT"
		}
		return ""
	}

	for _, path := range routes {
		rec := requestSub(t, router, http.MethodGet, path, "device-one", "")
		assertServesRealServers(t, path, rec)
		if rule := localRule(path); rule != "" && !strings.Contains(rec.Body.String(), rule) {
			t.Fatalf("%s: the registered device got no local-direct rule %s: %s", path, rule, rec.Body.String())
		}
	}
	for _, path := range routes {
		assertServesNotice(t, path, requestSub(t, router, http.MethodGet, path, "device-two", ""), limitOneNotice)
	}

	// The rules file names no server, and asking for it takes no slot.
	rec := requestSub(t, router, http.MethodGet, "/sub/"+subID+"/shadowrocket.conf", "device-two", "")
	if rec.Code != http.StatusOK || rec.Body.String() != plan.Shadowrocket() || strings.Contains(rec.Body.String(), hwidTestClientUUID) {
		t.Fatalf("shadowrocket.conf for a refused device = %d %q, want the plan's rules only", rec.Code, rec.Body.String())
	}
	assertHwidStatus(t, requestSub(t, router, http.MethodGet, "/sub/"+subID+"/hwid-status", "", ""), true, 1, 1, 0, true)
}
