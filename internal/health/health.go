// Package health turns periodic observations of the server (Xray up, restarts, memory, disk,
// certificates, backups) into operator alerts: one when a condition goes bad, an optional
// reminder while it stays bad, one when it is fixed — never one per check.
package health

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Phase is where an alert is in its life.
type Phase string

const (
	Raised   Phase = "raised"
	Reminder Phase = "reminder"
	Resolved Phase = "resolved"
)

// Check is one observation of one condition.
type Check struct {
	// Key identifies the condition across rounds, e.g. "disk:/" or "cert:inbound:in-443".
	Key string
	// Event is the notifier event type the alert is sent as, e.g. "disk.high".
	Event   string
	Problem bool
	// Confirm is how many problem observations in a row raise the alert (at least 1), so a
	// one-minute spike or a core that systemd brings back within seconds stays quiet.
	Confirm int
	// Params fill the message; the last problem's params also go with the resolved notice.
	Params map[string]string
}

// Notice is an alert to send.
type Notice struct {
	Key    string
	Event  string
	Phase  Phase
	Since  time.Time // when the condition was first observed bad
	Params map[string]string
}

type state struct {
	bad      int
	raised   bool
	since    time.Time
	lastSent time.Time
	params   map[string]string
}

// Tracker remembers each condition between rounds. It lives in memory: after a panel restart a
// condition that is still bad is announced once more.
type Tracker struct {
	mu     sync.Mutex
	states map[string]*state
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker { return &Tracker{states: map[string]*state{}} }

// Observe takes one round of checks and returns what to send. remind is how often a condition
// that stays bad is announced again (0: never). A key missing from the round is forgotten
// without a notice: what it watched is gone (an inbound deleted, backups turned off).
func (t *Tracker) Observe(now time.Time, remind time.Duration, checks []Check) []Notice {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Notice
	seen := make(map[string]bool, len(checks))
	for _, c := range checks {
		seen[c.Key] = true
		st := t.states[c.Key]
		if st == nil {
			st = &state{}
			t.states[c.Key] = st
		}
		notice := func(phase Phase, params map[string]string) {
			out = append(out, Notice{Key: c.Key, Event: c.Event, Phase: phase, Since: st.since, Params: maps.Clone(params)})
		}
		if !c.Problem {
			if st.raised {
				params := maps.Clone(st.params)
				if params == nil {
					params = map[string]string{}
				}
				maps.Copy(params, c.Params)
				notice(Resolved, params)
			}
			*st = state{}
			continue
		}
		if st.bad == 0 {
			st.since = now
		}
		st.bad++
		st.params = maps.Clone(c.Params)
		switch {
		case !st.raised && st.bad >= max(c.Confirm, 1):
			st.raised, st.lastSent = true, now
			notice(Raised, c.Params)
		case st.raised && remind > 0 && now.Sub(st.lastSent) >= remind:
			st.lastSent = now
			notice(Reminder, c.Params)
		}
	}
	for key := range t.states {
		if !seen[key] {
			delete(t.states, key)
		}
	}
	slices.SortStableFunc(out, func(a, b Notice) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// EventWindow counts events (Xray restarts) in a sliding window.
type EventWindow struct {
	mu    sync.Mutex
	times []time.Time
	keep  time.Duration
}

// NewEventWindow keeps events for at most keep.
func NewEventWindow(keep time.Duration) *EventWindow { return &EventWindow{keep: keep} }

// Add records an event at t.
func (w *EventWindow) Add(t time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.times = append(w.times, t)
	w.pruneLocked(t)
}

// Count returns how many events happened in the window ending at now.
func (w *EventWindow) Count(now time.Time, window time.Duration) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked(now)
	n := 0
	for _, t := range w.times {
		if now.Sub(t) <= window && !t.After(now) {
			n++
		}
	}
	return n
}

func (w *EventWindow) pruneLocked(now time.Time) {
	i := 0
	for i < len(w.times) && now.Sub(w.times[i]) > w.keep {
		i++
	}
	w.times = w.times[i:]
}

// ParseCertificate returns the first certificate of a PEM file.
func ParseCertificate(data []byte) (*x509.Certificate, error) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, errors.New("no PEM certificate found")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

// CertWarnWithin is how long before expiry a certificate is reported: days, but at most a
// quarter of its lifetime, so a 6-day certificate on an IP address that is renewed with two
// days left is not reported all the time.
func CertWarnWithin(notBefore, notAfter time.Time, days int) time.Duration {
	warn := time.Duration(days) * 24 * time.Hour
	if life := notAfter.Sub(notBefore); life > 0 {
		warn = min(warn, life/4)
	}
	return warn
}

// CertExpiring reports a certificate that has expired or expires within CertWarnWithin.
func CertExpiring(notBefore, notAfter, now time.Time, days int) bool {
	return notAfter.Sub(now) < CertWarnWithin(notBefore, notAfter, days)
}

// CgroupPath reads a process's cgroup v2 path from /proc/<pid>/cgroup ("0::/system.slice/x.service").
func CgroupPath(procCgroup string) (string, bool) {
	for line := range strings.SplitSeq(procCgroup, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok && strings.HasPrefix(rest, "/") {
			return rest, true
		}
	}
	return "", false
}

// ParseMemoryMax reads a cgroup memory.max: 0 for "max" (no limit) or anything unreadable.
func ParseMemoryMax(s string) uint64 {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// WorkingSet is a cgroup's memory.current without inactive file cache (memory.stat
// inactive_file): page cache the kernel drops before it would kill anything at MemoryMax.
func WorkingSet(current uint64, memoryStat string) uint64 {
	for line := range strings.SplitSeq(memoryStat, "\n") {
		if rest, ok := strings.CutPrefix(line, "inactive_file "); ok {
			if v, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 64); err == nil && v < current {
				return current - v
			}
		}
	}
	return current
}

// Percent is used/limit in percent, 0 when limit is 0.
func Percent(used, limit uint64) float64 {
	if limit == 0 {
		return 0
	}
	return float64(used) * 100 / float64(limit)
}
