package tgbot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
)

// shippedEnglish loads every bot message from the real en-US file, so the test
// checks the wording an owner actually receives, not a stand-in template.
func shippedEnglish(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRootForTest(t), "internal", "web", "translation", "en-US.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tgbot struct {
			Messages map[string]string `json:"messages"`
		} `json:"tgbot"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var msgs []*i18n.Message
	for k, v := range doc.Tgbot.Messages {
		msgs = append(msgs, &i18n.Message{ID: "tgbot.messages." + k, Other: v})
	}
	draftLocalizer(t, msgs...)
}

func withoutHostLine(s string) string {
	_, body, _ := strings.Cut(s, "\n")
	return body
}

// Each alert says what happened, why it matters and where to look; a key-like
// or log-like wording ("Xray CRASHED", "Error: ...") fails here.
func TestAlertsReadAsPlainNotes(t *testing.T) {
	shippedEnglish(t)
	bot := &Tgbot{}
	health := func(e eventbus.EventType, phase string, p map[string]string) string {
		return withoutHostLine(bot.formatHealthAlert(e, &eventbus.HealthAlertData{Phase: phase, Params: p}))
	}
	event := func(e eventbus.Event) string { return withoutHostLine(bot.formatEventMessage(e)) }

	cases := []struct{ name, got, want string }{
		{
			"xray restart loop",
			health(eventbus.EventXrayFlapping, "raised", map[string]string{"Count": "4", "Window": "10"}),
			"🔴 Xray restarted 4 times in 10 min. Each restart briefly drops every client. Check the Xray log in the panel.",
		},
		{
			"xray down",
			health(eventbus.EventXrayDown, "raised", map[string]string{"Error": "exit status 23"}),
			"🔴 Xray is not running, so clients cannot connect. Reason: exit status 23. Check the Xray log in the panel.",
		},
		{
			"disk full",
			health(eventbus.EventDiskHigh, "raised", map[string]string{"Path": "/", "Percent": "93", "Free": "1.4 GB"}),
			"🔴 Disk / is 93% full, only 1.4 GB left. When it fills up, the database, logs and backups stop being saved. Free some space, for example old logs and backups.",
		},
		{
			"certificate expiring",
			health(eventbus.EventCertExpiring, "raised", map[string]string{"Name": "example.com", "Where": "panel", "Date": "2026-12-01 10:00", "Days": "6", "Hours": "150"}),
			"🔴 Certificate example.com (panel) expires on 2026-12-01 10:00, in 6 days. After that, browsers and clients will refuse to connect. Renew it before then.",
		},
		{
			"backup failed",
			health(eventbus.EventBackupFailed, "raised", map[string]string{"Error": "no space left on device"}),
			"🔴 The backup was not saved, so recent changes are not protected. Reason: no space left on device. Check free disk space and the backup folder.",
		},
		{
			"node silent",
			health(eventbus.EventNodeSilent, "raised", map[string]string{"Name": "de-1", "Address": "203.0.113.5:2053", "Minutes": "12", "LastSeen": "2026-11-23 09:48"}),
			"🔴 Node de-1 (203.0.113.5:2053) has not answered for 12 min, last seen 2026-11-23 09:48. Its clients may have no access. Check that the server is up and reachable from the panel.",
		},
		{
			"xray crash",
			event(eventbus.Event{Type: eventbus.EventXrayCrash, Data: "exit status 1"}),
			"🔥 Xray stopped unexpectedly\nError: exit status 1\nClients cannot connect until it starts again. Check the Xray log in the panel.",
		},
		{
			"outbound down",
			event(eventbus.Event{Type: eventbus.EventOutboundDown, Source: "proxy-de", Data: &eventbus.OutboundHealthData{Error: "timeout"}}),
			"🔴 Outbound proxy-de is not reachable\nError: timeout\nTraffic routed through it fails until it recovers. Check the remote server and this outbound's settings.",
		},
		{
			"node down",
			event(eventbus.Event{Type: eventbus.EventNodeDown, Source: "de-1", Data: &eventbus.NodeHealthData{}}),
			"🔴 Node de-1 is not responding\nIts clients may lose access. Check that the node server is up and reachable from the panel.",
		},
		{
			"probe silent",
			event(eventbus.Event{Type: eventbus.EventProbeStale, Source: "home-1", Data: &eventbus.ProbeHealthData{Probe: "home-1", SilentFor: 31}}),
			"⚠️ Probe home-1 has not reported for 31 min, so blocks in its network go unnoticed. Check that the probe agent is running.",
		},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
}

// The memory alert reads the threshold from settings, so it needs a real DB.
func TestMemoryAlertReadsAsPlainNote(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	shippedEnglish(t)
	got := withoutHostLine((&Tgbot{}).formatEventMessage(eventbus.Event{
		Type: eventbus.EventMemoryHigh, Data: &eventbus.SystemMetricData{Percent: 91.53},
	}))
	want := "🔴 Memory use is 91.5%, above your 80% limit\nIf memory runs out, the system may kill Xray and drop clients. Check what uses memory on the server."
	if got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

// A failed login ends with what to do, after the who/where/when card.
func TestFailedLoginEndsWithAdvice(t *testing.T) {
	shippedEnglish(t)
	got := (&Tgbot{}).formatEventMessage(eventbus.Event{
		Type: eventbus.EventLoginAttempt, Source: "198.51.100.7",
		Data: &eventbus.LoginEventData{Username: "admin", IP: "198.51.100.7", Time: "2026-11-23 10:00", Status: "fail"},
	})
	want := "If this was not you, someone may be guessing the password. Keep it strong and watch for repeats."
	if !strings.HasSuffix(got, "\r\n"+want) {
		t.Errorf("login alert %q does not end with %q", got, want)
	}
}
