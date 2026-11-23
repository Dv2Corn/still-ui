package service

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const (
	// subLeakQuiet is how long an alerted subscription stays quiet unless its network count doubles.
	subLeakQuiet = 7 * 24 * time.Hour
	// subLeakMaxNetworks bounds the memory one subscription can take, whoever fetches it.
	subLeakMaxNetworks = 128
	subLeakExamples    = 5
	subLeakSweepEvery  = time.Hour
)

// subLeaks counts the client networks each subscription is fetched from; in memory only,
// so a panel restart starts every window afresh.
var subLeaks = newSubLeakWatch()

type subLeakSeen struct{ first, last time.Time }

type subLeakEntry struct {
	nets         map[netip.Prefix]subLeakSeen
	alertedAt    time.Time
	alertedCount int
}

type subLeakWatch struct {
	mu        sync.Mutex
	subs      map[string]*subLeakEntry
	lastSweep time.Time
}

// subLeakReport is what one alert says about a subscription.
type subLeakReport struct {
	Count       int
	First, Last time.Time
	Examples    []string // the most recently seen networks first
}

func newSubLeakWatch() *subLeakWatch {
	return &subLeakWatch{subs: map[string]*subLeakEntry{}}
}

// subLeakNetwork groups a client address by the network one subscriber is likely to use:
// /24 for IPv4 (an IPv4-mapped IPv6 address counts as IPv4), /48 for IPv6.
func subLeakNetwork(ip string) (netip.Prefix, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return netip.Prefix{}, false
	}
	addr = addr.Unmap().WithZone("")
	bits := 48
	if addr.Is4() {
		bits = 24
	}
	p, err := addr.Prefix(bits)
	return p, err == nil
}

// observe records one fetch and returns a report when it should alert: the first time the
// subscription reaches cfg.Networks in cfg.Window, then after subLeakQuiet or once the count doubles.
func (w *subLeakWatch) observe(subID string, network netip.Prefix, now time.Time, cfg SubLeakAlert) (subLeakReport, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := now.Add(-cfg.Window)
	if now.Sub(w.lastSweep) >= subLeakSweepEvery {
		w.lastSweep = now
		for id, e := range w.subs {
			e.prune(cutoff)
			if len(e.nets) == 0 && now.Sub(e.alertedAt) >= subLeakQuiet {
				delete(w.subs, id)
			}
		}
	}
	e := w.subs[subID]
	if e == nil {
		e = &subLeakEntry{nets: map[netip.Prefix]subLeakSeen{}}
		w.subs[subID] = e
	}
	e.prune(cutoff)
	if seen, ok := e.nets[network]; ok {
		seen.last = now
		e.nets[network] = seen
	} else {
		if len(e.nets) >= subLeakMaxNetworks {
			e.dropOldest()
		}
		e.nets[network] = subLeakSeen{first: now, last: now}
	}
	count := len(e.nets)
	if count < cfg.Networks {
		return subLeakReport{}, false
	}
	if !e.alertedAt.IsZero() && now.Sub(e.alertedAt) < subLeakQuiet && count < 2*e.alertedCount {
		return subLeakReport{}, false
	}
	e.alertedAt, e.alertedCount = now, count
	return e.report(), true
}

func (e *subLeakEntry) prune(cutoff time.Time) {
	for n, seen := range e.nets {
		if seen.last.Before(cutoff) {
			delete(e.nets, n)
		}
	}
}

func (e *subLeakEntry) dropOldest() {
	var oldest netip.Prefix
	var at time.Time
	for n, seen := range e.nets {
		if at.IsZero() || seen.last.Before(at) {
			oldest, at = n, seen.last
		}
	}
	delete(e.nets, oldest)
}

func (e *subLeakEntry) report() subLeakReport {
	nets := make([]netip.Prefix, 0, len(e.nets))
	r := subLeakReport{Count: len(e.nets)}
	for n, seen := range e.nets {
		nets = append(nets, n)
		if r.First.IsZero() || seen.first.Before(r.First) {
			r.First = seen.first
		}
		if seen.last.After(r.Last) {
			r.Last = seen.last
		}
	}
	slices.SortFunc(nets, func(a, b netip.Prefix) int {
		if c := e.nets[b].last.Compare(e.nets[a].last); c != 0 {
			return c
		}
		return strings.Compare(a.String(), b.String())
	})
	for _, n := range nets[:min(len(nets), subLeakExamples)] {
		r.Examples = append(r.Examples, n.String())
	}
	return r
}

func (w *subLeakWatch) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.subs) > 0 {
		w.subs = map[string]*subLeakEntry{}
	}
}

// RecordSubscriptionNetwork counts the network clientIP belongs to for subscription subID and
// raises the sub.leak alert once too many networks fetch it. It keeps nothing while the alert is off.
func RecordSubscriptionNetwork(subID, clientIP string) {
	cfg, on := (&SettingService{}).GetSubLeakAlert()
	if !on {
		subLeaks.reset()
		return
	}
	network, ok := subLeakNetwork(clientIP)
	if subID == "" || !ok {
		return
	}
	r, alert := subLeaks.observe(subID, network, time.Now(), cfg)
	if !alert {
		return
	}
	client := subLeakClientName(subID)
	hours := strconv.Itoa(int(cfg.Window / time.Hour))
	logger.Warningf("subscription of %s fetched from %d networks in %s h", client, r.Count, hours)
	if eventBus == nil {
		return
	}
	networks := strings.Join(r.Examples, ", ")
	if r.Count > len(r.Examples) {
		networks += ", …"
	}
	// The source is the client, never the subscription id: that id is the subscription's secret.
	eventBus.Publish(eventbus.Event{
		Type:   eventbus.EventSubLeak,
		Source: client,
		Data: &eventbus.HealthAlertData{Phase: "raised", Since: r.First, Params: map[string]string{
			"Client":   client,
			"Count":    strconv.Itoa(r.Count),
			"Hours":    hours,
			"First":    r.First.Local().Format("2006-01-02 15:04"),
			"Last":     r.Last.Local().Format("2006-01-02 15:04"),
			"Networks": networks,
		}},
	})
}

// subLeakClientName names a subscription's clients the way the panel lists them: email, then comment.
func subLeakClientName(subID string) string {
	var rows []model.ClientRecord
	if err := database.GetDB().Model(&model.ClientRecord{}).Select("email", "comment").
		Where("sub_id = ?", subID).Order("id").Find(&rows).Error; err != nil || len(rows) == 0 {
		return "?"
	}
	const shown = 3
	names := make([]string, 0, shown)
	for _, c := range rows[:min(len(rows), shown)] {
		name := c.Email
		if c.Comment != "" {
			name += " (" + c.Comment + ")"
		}
		names = append(names, name)
	}
	out := strings.Join(names, ", ")
	if len(rows) > shown {
		out += " +" + strconv.Itoa(len(rows)-shown)
	}
	return out
}
