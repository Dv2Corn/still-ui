package sub

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// remarkWorld is the probe world with a third inbound c (4433), the default remark template, and
// a traffic quota and expiry on every client, so the full remark shows email, traffic and days.
func remarkWorld(t *testing.T, mode string) *probeSubWorld {
	t.Helper()
	w := newProbeSubWorld(t, mode)
	seedSubInbound(t, "s1", "c", 4433, 3, probeTCPStream)
	db := database.GetDB()
	expiry := time.Now().Add(30 * 24 * time.Hour).UnixMilli()
	for _, email := range []string{"a@e", "b@e", "c@e"} {
		if err := db.Model(&model.ClientRecord{}).Where("email = ?", email).Update("expiry_time", expiry).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&xray.ClientTraffic{Email: email, Enable: true, Total: 10 << 30, ExpiryTime: expiry}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// fullRemark reports whether a remark carries the first-link information of the default template.
func fullRemark(remark string) bool {
	return strings.Contains(remark, "@e") && strings.Contains(remark, "📊") && strings.Contains(remark, "⏳")
}

func checkFirstRemarkOnly(t *testing.T, format string, remarks []string) {
	t.Helper()
	if len(remarks) < 2 {
		t.Fatalf("%s: remarks %q, want at least two entries", format, remarks)
	}
	if !fullRemark(remarks[0]) {
		t.Fatalf("%s: the first entry served lost the email/traffic/days remark: %q", format, remarks)
	}
	for _, r := range remarks[1:] {
		if fullRemark(r) || strings.Contains(r, "📊") {
			t.Fatalf("%s: a later entry carries the first-link remark: %q", format, remarks)
		}
	}
}

func rawRemarks(t *testing.T, w *probeSubWorld) []string {
	t.Helper()
	var out []string
	for _, l := range w.rawLinksWith(service.DefaultRemarkTemplate) {
		_, frag, _ := strings.Cut(l, "#")
		name, err := url.PathUnescape(frag)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	return out
}

func (w *probeSubWorld) rawLinksWith(remark string) []string {
	w.t.Helper()
	req := NewSubService(remark).ForRequest("req.example.com")
	req.subscriptionBody = true
	links, _, _, _, err := req.getSubs("s1")
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, l := range links {
		out = append(out, splitLinkLines(l)...)
	}
	return out
}

func clashRemarks(t *testing.T) []string {
	t.Helper()
	_, doc := renderClash(t, NewSubClashService(false, "", false, NewSubService(service.DefaultRemarkTemplate)))
	return proxyNamesOf(doc)
}

func jsonRemarks(t *testing.T) []string {
	t.Helper()
	out, _, err := NewSubJsonService("", "", "", "", NewSubService(service.DefaultRemarkTemplate)).GetJson("s1", "req.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	var remarks []string
	for _, doc := range parseSubJsonDocs(t, out) {
		r, _ := doc["remarks"].(string)
		remarks = append(remarks, r)
	}
	return remarks
}

// The full remark goes to the first entry the subscription serves after the probe ordering,
// in every format: hiding or moving the first link must not lose or misplace it.
func TestProbeOrderKeepsTheFirstLinkRemarkInFront(t *testing.T) {
	for _, mode := range []string{service.SubProbeOrderHide, service.SubProbeOrderReorder} {
		t.Run(mode, func(t *testing.T) {
			w := remarkWorld(t, mode)
			w.down(w.a, "203.0.113.5")
			want := 2
			if mode == service.SubProbeOrderReorder {
				want = 3
			}
			raw := rawRemarks(t, w)
			if len(raw) != want || !strings.HasPrefix(raw[0], "b-") {
				t.Fatalf("raw remarks %q, want b first and %d entries", raw, want)
			}
			checkFirstRemarkOnly(t, "raw", raw)
			clash := clashRemarks(t)
			if len(clash) != want || !strings.HasPrefix(clash[0], "b-") {
				t.Fatalf("clash proxies %q, want b first and %d entries", clash, want)
			}
			checkFirstRemarkOnly(t, "clash", clash)
			js := jsonRemarks(t)
			if len(js) != want || !strings.HasPrefix(js[0], "b-") {
				t.Fatalf("json remarks %q, want b first and %d entries", js, want)
			}
			checkFirstRemarkOnly(t, "json", js)
		})
	}

	// Nothing down: the normal first link keeps it, without a second render.
	t.Run("nothing down", func(t *testing.T) {
		w := remarkWorld(t, service.SubProbeOrderHide)
		raw := rawRemarks(t, w)
		if !strings.HasPrefix(raw[0], "a-") {
			t.Fatalf("raw remarks %q", raw)
		}
		checkFirstRemarkOnly(t, "raw", raw)
		checkFirstRemarkOnly(t, "clash", clashRemarks(t))
		checkFirstRemarkOnly(t, "json", jsonRemarks(t))
	})

	// Hosts-first ordering renders entries in their final order; the rule still holds.
	t.Run("hosts first", func(t *testing.T) {
		w := remarkWorld(t, service.SubProbeOrderHide)
		setSubSetting(t, "subHostSortMode", "hosts-first")
		w.down(w.a, "203.0.113.5")
		checkFirstRemarkOnly(t, "raw", rawRemarks(t, w))
		checkFirstRemarkOnly(t, "clash", clashRemarks(t))
		checkFirstRemarkOnly(t, "json", jsonRemarks(t))
	})
}

// A censor blocking a whole server makes a probe that only checks that server fail on every
// endpoint. With its direct check passing, subscriptions still act on it.
func TestProbeOrderActsOnAWholeServerBlock(t *testing.T) {
	w := newProbeSubWorld(t, service.SubProbeOrderHide)
	c := seedSubInbound(t, "s1", "c", 4433, 3, probeTCPStream)
	db := database.GetDB()
	if err := db.Model(c).Update("listen", "198.51.100.7").Error; err != nil {
		t.Fatal(err)
	}
	// The probe checks only the blocked server; its control target goes away.
	w.agent.InboundIds = []int{w.a.Id, w.b.Id}
	if err := db.Save(w.agent).Error; err != nil {
		t.Fatal(err)
	}
	w.state(service.ReachDirectKey, 0)
	w.down(w.a, "203.0.113.5")
	w.down(w.b, "203.0.113.5")
	if got := linkPorts(w.rawLinks()); got != "198.51.100.7:4433" {
		t.Fatalf("links = %q, want only the other server's link", got)
	}

	// With the probe's own direct check failing too, its network is the likely cause.
	if err := db.Model(&model.ReachProbeTarget{}).Where("probe_id = ? AND target_key = ?", w.agent.Id, service.ReachDirectKey).
		Updates(map[string]any{"consecutive_fails": 1}).Error; err != nil {
		t.Fatal(err)
	}
	resetProbeVerdictCache()
	if got := linkPorts(w.rawLinks()); got != "203.0.113.5:4431 203.0.113.5:4432 198.51.100.7:4433" {
		t.Fatalf("links = %q, want the normal list", got)
	}
}
