package email

import (
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
)

// A node, an outbound or a login form chooses these values, so the HTML alert must show
// them as text: raw, they let a rogue node plant a fake sign-in link in the admin's mail.
func TestFormatMessageEscapesReportedValues(t *testing.T) {
	const payload = `<a href="https://attacker.invalid/login">sign in</a>`
	const escaped = `&lt;a href=&#34;https://attacker.invalid/login&#34;&gt;sign in&lt;/a&gt;`
	_, body := (&Subscriber{}).formatMessage(eventbus.Event{
		Type: eventbus.EventNodeDown, Source: payload, Timestamp: time.Unix(0, 0),
		Data: &eventbus.NodeHealthData{NodeId: 1, XrayError: payload},
	})
	if strings.Contains(body, "<a href") || strings.Count(body, escaped) != 2 {
		t.Fatalf("node-down body carries the node's markup or lacks it as text:\n%s", body)
	}
	if !strings.Contains(body, `<span style="color:red">`) {
		t.Fatalf("node-down body lost the panel's own status markup:\n%s", body)
	}
}

// The login publisher escapes the username for Telegram, so the mail must not show it twice escaped.
func TestFormatMessageShowsTheLoginNameOnce(t *testing.T) {
	_, body := (&Subscriber{}).formatMessage(eventbus.Event{
		Type: eventbus.EventLoginAttempt, Timestamp: time.Unix(0, 0),
		Data: &eventbus.LoginEventData{Username: "a&amp;b&lt;", IP: "203.0.113.5", Status: "success"},
	})
	if !strings.Contains(body, "a&amp;b&lt;") || strings.Contains(body, "&amp;amp;") {
		t.Fatalf("login body = %s; want the username escaped exactly once", body)
	}
}
