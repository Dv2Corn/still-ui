package job

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type silenceStep struct {
	at      time.Duration // fake clock offset from the test's base time
	probeOK bool
	restart bool // a fresh job, as after a panel restart
}

// Each case drives the real heartbeat job against a node that answers or not, on a
// fake clock, and lists the node.silent notices it published.
func TestNodeSilenceAlert(t *testing.T) {
	cases := []struct {
		name      string
		minutes   int
		disabled  bool
		startedAt time.Duration // process start, relative to base
		steps     []silenceStep
		want      []string
	}{
		{
			name:    "silent past the threshold alerts once, even across a restart",
			minutes: 30, startedAt: -time.Hour,
			steps: []silenceStep{
				{at: 0, probeOK: true},
				{at: 10 * time.Minute},
				{at: 31 * time.Minute},
				{at: 40 * time.Minute},
				{at: 5 * time.Hour, restart: true},
			},
			want: []string{"step 3: raised 31 min"},
		},
		{
			name:    "startup grace holds the alert until ten minutes after the process start",
			minutes: 30, startedAt: 25 * time.Minute,
			steps: []silenceStep{
				{at: 0, probeOK: true},
				{at: 31 * time.Minute},
				{at: 34 * time.Minute},
				{at: 36 * time.Minute},
			},
			want: []string{"step 4: raised 36 min"},
		},
		{
			name:    "recovery sends one message with the outage duration",
			minutes: 30, startedAt: -time.Hour,
			steps: []silenceStep{
				{at: 0, probeOK: true},
				{at: 31 * time.Minute},
				{at: 50 * time.Minute, probeOK: true},
				{at: 55 * time.Minute, probeOK: true},
			},
			want: []string{"step 2: raised 31 min", "step 3: resolved 50 min"},
		},
		{
			name:    "a second outage within six hours of the alert stays quiet",
			minutes: 30, startedAt: -time.Hour,
			steps: []silenceStep{
				{at: 0, probeOK: true},
				{at: 31 * time.Minute},
				{at: 40 * time.Minute, probeOK: true},
				{at: 75 * time.Minute},
				{at: 80 * time.Minute, probeOK: true},
				{at: 6*time.Hour + 40*time.Minute},
			},
			want: []string{"step 2: raised 31 min", "step 3: resolved 40 min", "step 6: raised 320 min"},
		},
		{
			name:    "a disabled node never alerts",
			minutes: 30, startedAt: -time.Hour, disabled: true,
			steps: []silenceStep{
				{at: 0, probeOK: true},
				{at: 31 * time.Minute},
				{at: 2 * time.Hour},
			},
		},
		{
			name:    "threshold 0 turns the alert off",
			minutes: 0, startedAt: -time.Hour,
			steps: []silenceStep{
				{at: 0, probeOK: true},
				{at: 31 * time.Minute},
				{at: 2 * time.Hour},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := switchableNode(t)
			if err := (&service.SettingService{}).SetNodeSilenceMinutes(tc.minutes); err != nil {
				t.Fatalf("SetNodeSilenceMinutes: %v", err)
			}
			events := collectNodeEvents(t)
			base := time.Now().Truncate(time.Second)
			var now time.Time
			newJob := func() *NodeHeartbeatJob {
				j := NewNodeHeartbeatJob()
				j.now = func() time.Time { return now }
				j.startedAt = base.Add(tc.startedAt)
				return j
			}
			job := newJob()

			var got []string
			seen := 0
			for i, step := range tc.steps {
				now = base.Add(step.at)
				if step.restart {
					job = newJob()
				}
				if i == 1 && tc.disabled {
					if err := database.GetDB().Model(&model.Node{}).Where("name = ?", "flaky").Update("enable", false).Error; err != nil {
						t.Fatalf("disable node: %v", err)
					}
				}
				up.Store(step.probeOK)
				job.Run()
				all := events()
				for _, e := range all[seen:] {
					if e.Type != eventbus.EventNodeSilent {
						continue
					}
					data := e.Data.(*eventbus.HealthAlertData)
					got = append(got, fmt.Sprintf("step %d: %s %s min", i+1, data.Phase, data.Params["Minutes"]))
				}
				seen = len(all)
			}

			if strings.Join(got, ", ") != strings.Join(tc.want, ", ") {
				t.Fatalf("notices = %q, want %q", got, tc.want)
			}
		})
	}
}
