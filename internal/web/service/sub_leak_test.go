package service

import (
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
)

func TestSubLeakNetworkGroupsAddresses(t *testing.T) {
	cases := []struct {
		ip, want string
	}{
		{"203.0.113.7", "203.0.113.0/24"},
		{"203.0.113.250", "203.0.113.0/24"},
		{"::ffff:203.0.113.9", "203.0.113.0/24"},
		{"198.51.100.1", "198.51.100.0/24"},
		{"2001:db8:1:2::5", "2001:db8:1::/48"},
		{"2001:db8:1:ffff:1:2:3:4", "2001:db8:1::/48"},
		{"2001:db8:2::1", "2001:db8:2::/48"},
		{"fe80::1%eth0", "fe80::/48"},
		{"unknown", ""},
		{"", ""},
	}
	for _, c := range cases {
		got, ok := subLeakNetwork(c.ip)
		if c.want == "" {
			if ok {
				t.Errorf("%q grouped as %s, want no network", c.ip, got)
			}
			continue
		}
		if !ok || got.String() != c.want {
			t.Errorf("%q grouped as %s (%v), want %s", c.ip, got, ok, c.want)
		}
	}
}

func leakNet(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, ok := subLeakNetwork(s)
	if !ok {
		t.Fatalf("no network for %q", s)
	}
	return p
}

func TestSubLeakWatchCountsNetworksInTheWindow(t *testing.T) {
	w := newSubLeakWatch()
	cfg := SubLeakAlert{Networks: 3, Window: 24 * time.Hour}
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	seen := func(sub, ip string, at time.Duration) (subLeakReport, bool) {
		return w.observe(sub, leakNet(t, ip), t0.Add(at), cfg)
	}
	steps := []struct {
		sub, ip string
		at      time.Duration
	}{
		{"a", "203.0.113.1", 0},
		{"a", "198.51.100.1", time.Hour},
		{"a", "203.0.113.99", 2 * time.Hour}, // same /24 as the first fetch
		{"b", "192.0.2.1", 2 * time.Hour},    // another subscription counts on its own
		{"b", "2001:db8:7::1", 2 * time.Hour},
		// 30 h after the first fetch both earlier networks of "a" are out of the window.
		{"a", "2001:db8:1::1", 30 * time.Hour},
		{"a", "2001:db8:2::1", 31 * time.Hour},
	}
	for _, s := range steps {
		if r, alert := seen(s.sub, s.ip, s.at); alert {
			t.Fatalf("%s from %s at +%s alerted with %+v, below 3 networks in 24 h", s.sub, s.ip, s.at, r)
		}
	}
	r, alert := seen("a", "::ffff:192.0.2.77", 32*time.Hour)
	if !alert {
		t.Fatal("the third network within 24 h did not alert")
	}
	want := subLeakReport{
		Count:    3,
		First:    t0.Add(30 * time.Hour),
		Last:     t0.Add(32 * time.Hour),
		Examples: []string{"192.0.2.0/24", "2001:db8:2::/48", "2001:db8:1::/48"},
	}
	if r.Count != want.Count || !r.First.Equal(want.First) || !r.Last.Equal(want.Last) || !slices.Equal(r.Examples, want.Examples) {
		t.Fatalf("report = %+v, want %+v", r, want)
	}
}

func TestSubLeakWatchAlertsOnceUntilDoubledOrAWeekLater(t *testing.T) {
	w := newSubLeakWatch()
	cfg := SubLeakAlert{Networks: 2, Window: 24 * time.Hour}
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	next := 0
	fetch := func(at time.Time) (subLeakReport, bool) {
		next++
		return w.observe("s", leakNet(t, "10.0."+strconv.Itoa(next)+".1"), at, cfg)
	}
	alerts := func(at time.Time, n int) []int {
		var got []int
		for range n {
			if r, ok := fetch(at); ok {
				got = append(got, r.Count)
			}
		}
		return got
	}
	if got := alerts(t0, 4); !slices.Equal(got, []int{2, 4}) {
		t.Fatalf("alerts on the first four networks at counts %v, want [2 4]: once, then on doubling", got)
	}
	// Six and a half days on, the earlier networks are out of the window: two new ones are no news.
	if got := alerts(t0.Add(156*time.Hour), 2); len(got) != 0 {
		t.Fatalf("alerted again within a week at counts %v", got)
	}
	if got := alerts(t0.Add(7*24*time.Hour+time.Minute), 1); !slices.Equal(got, []int{3}) {
		t.Fatalf("a week after the last alert, counts %v, want [3]", got)
	}
}

func TestSubLeakWatchStaysBounded(t *testing.T) {
	w := newSubLeakWatch()
	cfg := SubLeakAlert{Networks: 64, Window: 24 * time.Hour}
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := range 1000 {
		ip := "2001:db8:" + strconv.FormatInt(int64(i), 16) + "::1"
		w.observe("s", leakNet(t, ip), t0.Add(time.Duration(i)*time.Second), cfg)
	}
	if got := len(w.subs["s"].nets); got != subLeakMaxNetworks {
		t.Fatalf("one subscription keeps %d networks, want the cap %d", got, subLeakMaxNetworks)
	}
	// A subscription nobody fetches any more is forgotten once its networks and its alert age out.
	w.observe("t", leakNet(t, "192.0.2.1"), t0.Add(8*24*time.Hour), cfg)
	if _, kept := w.subs["s"]; kept {
		t.Fatal("an idle subscription stays in memory after its window and the quiet week")
	}
}

func setSubLeakSettings(t *testing.T, botOn bool, events string, networks int) {
	t.Helper()
	s := &SettingService{}
	for k, v := range map[string]string{
		"tgBotEnable":     strconv.FormatBool(botOn),
		"tgEnabledEvents": events,
		"subLeakNetworks": strconv.Itoa(networks),
	} {
		if err := s.saveSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecordSubscriptionNetworkPublishesOnlyWhenTheAlertIsOn(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	saved := subLeaks
	subLeaks = newSubLeakWatch()
	t.Cleanup(func() { subLeaks = saved })
	bus := eventbus.New(16)
	got := make(chan eventbus.Event, 16)
	bus.Subscribe("test", func(e eventbus.Event) { got <- e })
	SetEventBus(bus)
	t.Cleanup(func() { SetEventBus(nil); bus.Stop() })

	db := database.GetDB()
	for _, c := range []*model.ClientRecord{
		{Email: "alice@shop", SubID: "sub-1", Comment: "VIP <paid>", Enable: true},
		{Email: "alice-hy2@shop", SubID: "sub-1", Enable: true},
	} {
		if err := db.Create(c).Error; err != nil {
			t.Fatal(err)
		}
	}
	fetchFrom := func(ips ...string) {
		for _, ip := range ips {
			RecordSubscriptionNetwork("sub-1", ip)
		}
	}

	// The event is ticked but the bot is off: nothing is counted.
	setSubLeakSettings(t, false, "login.attempt,sub.leak", 2)
	fetchFrom("203.0.113.1", "198.51.100.1")
	// The bot is on but the event is not ticked.
	setSubLeakSettings(t, true, "login.attempt", 2)
	fetchFrom("192.0.2.1", "192.0.3.1")
	if n := len(subLeaks.subs); n != 0 {
		t.Fatalf("the alert is off but %d subscription(s) are tracked", n)
	}

	setSubLeakSettings(t, true, "login.attempt,sub.leak", 2)
	fetchFrom("203.0.113.1", "203.0.113.2", "2001:db8:5::1")
	select {
	case e := <-got:
		data, ok := e.Data.(*eventbus.HealthAlertData)
		if e.Type != eventbus.EventSubLeak || !ok {
			t.Fatalf("event %s %T, want sub.leak with health alert data", e.Type, e.Data)
		}
		want := map[string]string{
			"Client":   "alice@shop (VIP <paid>), alice-hy2@shop",
			"Count":    "2",
			"Hours":    "24",
			"Networks": "2001:db8:5::/48, 203.0.113.0/24",
		}
		for k, v := range want {
			if data.Params[k] != v {
				t.Errorf("param %s = %q, want %q", k, data.Params[k], v)
			}
		}
		if data.Params["First"] == "" || data.Params["Last"] == "" || data.Phase != "raised" {
			t.Errorf("alert = %+v, want first and last seen and phase raised", data)
		}
		if e.Source == "sub-1" {
			t.Error("the event names the subscription id, which is the subscription's secret")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no alert for two networks with a threshold of 2")
	}
	select {
	case e := <-got:
		t.Fatalf("a second alert %+v right after the first", e)
	case <-time.After(100 * time.Millisecond):
	}

	setSubLeakSettings(t, true, "login.attempt", 2)
	fetchFrom("192.0.2.1")
	if n := len(subLeaks.subs); n != 0 {
		t.Fatalf("turning the alert off kept %d subscription(s) in memory", n)
	}
}
