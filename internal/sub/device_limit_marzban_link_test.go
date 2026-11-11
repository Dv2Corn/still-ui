package sub

import (
	"net/http"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/panelimport/marzbantest"
)

// An imported Marzban user under the panel's default device limit: the link Marzban gave is
// gated like the user's own link, and the two count the same devices.
func TestDeviceLimitGatesOldMarzbanLinks(t *testing.T) {
	importMarzbanFixture(t)
	setSubSetting(t, "subHwidDefaultLimit", "1")
	router := newSubscriptionTestRouter(subscriptionTestRouterConfig{})
	oldLink := "/sub/" + marzbantest.AliceToken
	ownLink := "/sub/" + subIDOf(t, "alice")

	served := func(path, device string) []string {
		return serverNames(t, path, requestSub(t, router, http.MethodGet, path, device, ""))
	}
	for _, path := range []string{oldLink, ownLink} {
		if names := served(path, "device-one"); len(names) == 0 || slices.Contains(names, limitOneNotice) {
			t.Fatalf("%s for the registered device = %q, want alice's servers", path, names)
		}
	}
	for _, path := range []string{oldLink, ownLink} {
		assertServesNotice(t, path, requestSub(t, router, http.MethodGet, path, "device-two", ""), limitOneNotice)
	}
}
