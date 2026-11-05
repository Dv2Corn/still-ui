package tgbot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
)

var templateParam = regexp.MustCompile(`\{\{\s*\.(\w+)\s*\}\}`)

func params(s string) []string {
	var out []string
	for _, m := range templateParam.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// The health alert messages are looked up by a key built at run time, so the scan for
// I18nBot("...") literals does not see them: every locale must carry each one, with the
// same template parameters as English (a misspelt one renders as "<no value>").
func TestHealthAlertMessagesInEveryLocale(t *testing.T) {
	keys := []string{"alertCertExpired", "alertStill", "alertNodeTrafficWarn", "alertNodeTrafficLimit", "alertNodeTrafficLimitOff", "alertNodeTrafficBack"}
	for _, pair := range healthAlertKeys {
		keys = append(keys, pair[0], pair[1])
	}
	dir := filepath.Join(repoRootForTest(t), "internal", "web", "translation")
	read := func(name string) map[string]string {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Tgbot struct {
				Messages map[string]string `json:"messages"`
			} `json:"tgbot"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return doc.Tgbot.Messages
	}
	english := read("en-US.json")
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) < 2 {
		t.Fatal("no translations found")
	}
	for _, f := range files {
		msgs := read(filepath.Base(f))
		for _, k := range keys {
			v, ok := msgs[k]
			if !ok || v == "" {
				t.Errorf("%s: tgbot.messages.%s is missing", filepath.Base(f), k)
				continue
			}
			if got, want := params(v), params(english[k]); !slices.Equal(got, want) {
				t.Errorf("%s: tgbot.messages.%s uses %v, English uses %v", filepath.Base(f), k, got, want)
			}
		}
	}
}

func TestHealthAlertMessageChoice(t *testing.T) {
	cases := []struct {
		event eventbus.EventType
		data  *eventbus.HealthAlertData
		want  string
	}{
		{eventbus.EventDiskHigh, &eventbus.HealthAlertData{Phase: "raised"}, "alertDisk"},
		{eventbus.EventDiskHigh, &eventbus.HealthAlertData{Phase: "reminder"}, "alertDisk"},
		{eventbus.EventDiskHigh, &eventbus.HealthAlertData{Phase: "resolved"}, "alertDiskResolved"},
		{eventbus.EventCertExpiring, &eventbus.HealthAlertData{Phase: "raised", Params: map[string]string{"Hours": "30"}}, "alertCertExpiring"},
		{eventbus.EventCertExpiring, &eventbus.HealthAlertData{Phase: "raised", Params: map[string]string{"Hours": "-2"}}, "alertCertExpired"},
		{eventbus.EventCertExpiring, &eventbus.HealthAlertData{Phase: "resolved", Params: map[string]string{"Hours": "2000"}}, "alertCertRenewed"},
		{eventbus.EventCPUHigh, &eventbus.HealthAlertData{Phase: "raised"}, ""},
		{eventbus.EventNodeTraffic, &eventbus.HealthAlertData{Phase: "raised", Params: map[string]string{"Level": "warn", "Action": "disable"}}, "alertNodeTrafficWarn"},
		{eventbus.EventNodeTraffic, &eventbus.HealthAlertData{Phase: "raised", Params: map[string]string{"Level": "limit", "Action": "notify"}}, "alertNodeTrafficLimit"},
		{eventbus.EventNodeTraffic, &eventbus.HealthAlertData{Phase: "raised", Params: map[string]string{"Level": "limit", "Action": "disable"}}, "alertNodeTrafficLimitOff"},
		{eventbus.EventNodeTraffic, &eventbus.HealthAlertData{Phase: "resolved", Params: map[string]string{"Level": "under", "Action": "disable"}}, "alertNodeTrafficBack"},
	}
	for _, c := range cases {
		if got, _ := healthAlertMessage(c.event, c.data); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.event, c.data.Phase, got, c.want)
		}
	}
}
