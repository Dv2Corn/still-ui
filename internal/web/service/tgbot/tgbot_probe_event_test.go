package tgbot

import (
	"strings"
	"testing"

	"github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
)

// Probe alerts carry an inbound remark and an error the agent wrote; Telegram
// parses them as HTML, so they must arrive escaped exactly once and on one line.
func TestProbeAlertsAreEscapedAndWordedPerTransition(t *testing.T) {
	draftLocalizer(t,
		&i18n.Message{ID: "tgbot.messages.eventProbeDown", Other: "Probe {{ .Probe }}: {{ .Target }} is NOT reachable"},
		&i18n.Message{ID: "tgbot.messages.eventProbeUp", Other: "Probe {{ .Probe }}: {{ .Target }} is reachable again"},
		&i18n.Message{ID: "tgbot.messages.eventProbeBack", Other: "Probe {{ .Probe }} is reporting again"},
		&i18n.Message{ID: "tgbot.messages.eventProbeStale", Other: "Probe {{ .Probe }} has not reported for {{ .Minutes }} min"},
		&i18n.Message{ID: "tgbot.messages.eventErrorDetail", Other: "Error: {{ .Error }}"},
		&i18n.Message{ID: "tgbot.messages.eventDelayDetail", Other: "Delay: {{ .Delay }}ms"},
	)
	format := func(eventType eventbus.EventType, data *eventbus.ProbeHealthData) string {
		return (&Tgbot{}).formatEventMessage(eventbus.Event{Type: eventType, Source: data.Probe, Data: data})
	}

	down := format(eventbus.EventProbeDown, &eventbus.ProbeHealthData{
		Probe: "home-1", Target: "Main <b>&", Error: "stalled after 16384 bytes\n<i>timeout</i>",
	})
	for _, want := range []string{
		"🔴 Probe home-1: Main &lt;b&gt;&amp; is NOT reachable",
		"\nError: stalled after 16384 bytes &lt;i&gt;timeout&lt;/i&gt;",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("down alert %q lacks %q", down, want)
		}
	}
	if strings.Contains(down, "&amp;lt;") {
		t.Errorf("down alert is escaped twice: %q", down)
	}

	up := format(eventbus.EventProbeUp, &eventbus.ProbeHealthData{Probe: "home-1", Target: "Main", LatencyMs: 140})
	if !strings.Contains(up, "🟢 Probe home-1: Main is reachable again\nDelay: 140ms") {
		t.Errorf("up alert %q", up)
	}
	back := format(eventbus.EventProbeUp, &eventbus.ProbeHealthData{Probe: "home-1"})
	if !strings.Contains(back, "🟢 Probe home-1 is reporting again") || strings.Contains(back, "reachable") {
		t.Errorf("probe-back alert %q", back)
	}
	stale := format(eventbus.EventProbeStale, &eventbus.ProbeHealthData{Probe: "home-1", SilentFor: 31})
	if !strings.Contains(stale, "⚠️ Probe home-1 has not reported for 31 min") {
		t.Errorf("stale alert %q", stale)
	}
}
