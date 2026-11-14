package sub

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// The leak alert counts the network of every fetch that handed out the subscription, its page
// or a refused device's notice, by the address the panel trusts: proxy headers only from trustedProxyCIDRs.
func TestSubscriptionFetchNetworksFeedTheLeakAlert(t *testing.T) {
	router, subID := initHwidSubRouter(t, 1)
	// A fetch while the alert is off clears what an earlier run counted (-count, -shuffle).
	service.RecordSubscriptionNetwork(subID, "192.0.2.1")
	setSubSetting(t, "tgBotEnable", "true")
	setSubSetting(t, "tgEnabledEvents", "sub.leak")
	setSubSetting(t, "subLeakNetworks", "3")
	bus := eventbus.New(16)
	alerts := make(chan eventbus.Event, 16)
	bus.Subscribe("test", func(e eventbus.Event) { alerts <- e })
	service.SetEventBus(bus)
	t.Cleanup(func() { service.SetEventBus(nil); bus.Stop() })

	fetch := func(path, remote, forwardedFor, hwid, accept string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "sub.example.com"
		req.RemoteAddr = remote
		if forwardedFor != "" {
			req.Header.Set("X-Forwarded-For", forwardedFor)
		}
		if hwid != "" {
			req.Header.Set("X-HWID", hwid)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s from %s = %d, want 200", path, remote, rec.Code)
		}
		return rec
	}
	path := "/sub/" + subID
	assertServesRealServers(t, path, fetch(path, "203.0.113.5:40000", "", "device-one", ""))
	// A second device behind the loopback proxy is refused, and its network still counts.
	assertServesNotice(t, path, fetch(path, "127.0.0.1:40001", "198.51.100.7", "device-two", ""), limitOneNotice)
	// The device count page hands out no servers and is not counted.
	fetch(path+"?format=info", "100.64.0.1:40002", "", "", "")
	select {
	case e := <-alerts:
		t.Fatalf("alert %+v at two networks with a threshold of 3", e.Data)
	case <-time.After(50 * time.Millisecond):
	}
	// The subscription page from a client outside trustedProxyCIDRs: its X-Forwarded-For is its own claim.
	fetch(path, "[::ffff:192.0.2.10]:40003", "10.9.9.9", "", "text/html")

	select {
	case e := <-alerts:
		data, _ := e.Data.(*eventbus.HealthAlertData)
		if e.Type != eventbus.EventSubLeak || data == nil {
			t.Fatalf("event %s %T, want sub.leak", e.Type, e.Data)
		}
		if got, want := data.Params["Networks"], "192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24"; got != want || data.Params["Count"] != "3" {
			t.Fatalf("alert counts %s networks %q, want 3: %q", data.Params["Count"], got, want)
		}
		if data.Params["Client"] != "route@example.com" {
			t.Fatalf("alert names %q, want the client's email", data.Params["Client"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no alert after three networks")
	}
}
