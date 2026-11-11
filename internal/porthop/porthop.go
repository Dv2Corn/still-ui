// Package porthop redirects the UDP port ranges Hysteria2 clients hop over to the
// inbound's own port, with one nftables table the panel owns.
package porthop

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/nfttable"
)

// TableName is the inet table the panel owns; nothing else is ever touched.
const TableName = "still_port_hop"

// MinPort keeps a hop range off the well-known ports, where the server's own services answer.
const MinPort = 1024

// maxRanges bounds the work per inbound: the panel looks into every entry on each pass.
const maxRanges = 32

// presenceCheckEvery is how often an unchanged table is looked up again: a reboot or a
// foreign "nft flush ruleset" removes it without the panel knowing.
const presenceCheckEvery = time.Minute

// ErrNotPermitted means nft is installed but this process may not change the ruleset.
var ErrNotPermitted = errors.New("nftables rules cannot be changed here: the panel lacks the NET_ADMIN capability (common in containers)")

// Range is an inclusive port range; a single port has From == To.
type Range struct {
	From, To uint16
}

func (r Range) String() string {
	if r.From == r.To {
		return strconv.Itoa(int(r.From))
	}
	return fmt.Sprintf("%d-%d", r.From, r.To)
}

// Contains reports whether port lies in the range.
func (r Range) Contains(port int) bool { return port >= int(r.From) && port <= int(r.To) }

// Overlaps reports whether the two ranges share a port.
func (r Range) Overlaps(o Range) bool { return r.From <= o.To && o.From <= r.To }

// ParseRanges reads a hop range as clients write it: "20000-50000" or "20000-30000,40000".
func ParseRanges(spec string) ([]Range, error) {
	var out []Range
	for part := range strings.SplitSeq(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("port range %q has an empty entry", spec)
		}
		loS, hiS, isRange := strings.Cut(part, "-")
		if !isRange {
			hiS = loS
		}
		lo, errLo := strconv.Atoi(strings.TrimSpace(loS))
		hi, errHi := strconv.Atoi(strings.TrimSpace(hiS))
		if errLo != nil || errHi != nil || lo > hi || hi > 65535 {
			return nil, fmt.Errorf("port range %q is not a port or a low-high range", part)
		}
		if lo < MinPort {
			return nil, fmt.Errorf("port range %q starts below %d", part, MinPort)
		}
		r := Range{From: uint16(lo), To: uint16(hi)}
		// A repeated port is a typo; refuse it rather than rely on how nft merges intervals.
		if slices.ContainsFunc(out, r.Overlaps) {
			return nil, fmt.Errorf("port range %q repeats ports of an earlier entry", part)
		}
		if len(out) == maxRanges {
			return nil, fmt.Errorf("port range %q has more than %d entries", spec, maxRanges)
		}
		out = append(out, r)
	}
	return out, nil
}

// Redirect sends UDP arriving on Ranges to the local port To.
type Redirect struct {
	Ranges []Range
	To     uint16
}

func portElements(ports []uint16) string {
	ports = slices.Clone(ports)
	slices.Sort(ports)
	ports = slices.DeleteFunc(slices.Compact(ports), func(p uint16) bool { return p == 0 })
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, strconv.Itoa(int(p)))
	}
	return strings.Join(parts, ", ")
}

// Ruleset returns one nft transaction that replaces the table. Only UDP addressed to this
// host is redirected; protected ports keep their own listeners whatever the ranges say.
func Ruleset(redirects []Redirect, protected []uint16) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("add table inet %s\ndelete table inet %s", TableName, TableName)
	w("table inet %s {", TableName)
	w("\tcounter redirected {}")
	if elements := portElements(protected); elements != "" {
		w("\tset protected { type inet_service; elements = { %s }; }", elements)
	} else {
		w("\tset protected { type inet_service; }")
	}
	// Ahead of other dstnat chains, so a range never depends on the order tables were loaded in.
	w("\tchain prerouting {\n\t\ttype nat hook prerouting priority dstnat - 10; policy accept;")
	// Only traffic addressed to this host: what it routes onward (kernel-mode AmneziaWG clients) is left alone.
	w("\t\tfib daddr type != local return")
	w("\t\tudp dport @protected return")
	redirects = slices.Clone(redirects)
	slices.SortFunc(redirects, func(a, b Redirect) int { return int(a.To) - int(b.To) })
	for _, r := range redirects {
		parts := make([]string, 0, len(r.Ranges))
		for _, rng := range r.Ranges {
			parts = append(parts, rng.String())
		}
		w("\t\tudp dport { %s } counter name redirected redirect to :%d", strings.Join(parts, ", "), r.To)
	}
	w("\t}\n}")
	return b.String()
}

// Status describes the host side of the redirect.
type Status struct {
	Supported    bool `json:"supported" example:"true"`
	NftInstalled bool `json:"nftInstalled" example:"true"`
	// Permitted is false when nft runs but may not change rules (no NET_ADMIN).
	Permitted bool `json:"permitted" example:"true"`
	Active    bool `json:"active" example:"true"`
	// Packets counts first packets of redirected flows since the table was last loaded.
	Packets uint64 `json:"packets" example:"1204"`
}

// Manager applies and removes the table; safe for concurrent use.
type Manager struct {
	mu      sync.Mutex
	t       *nfttable.Table
	applied string
	checked time.Time
	// cleaned records that a table left by an earlier run has been removed.
	cleaned bool
	now     func() time.Time
}

func newManager(r nfttable.Runner, goos string) *Manager {
	return &Manager{t: &nfttable.Table{Name: TableName, R: r, GOOS: goos}, now: time.Now}
}

var defaultManager = &Manager{t: nfttable.New(TableName), now: time.Now}

// Default is the manager for the real host.
func Default() *Manager { return defaultManager }

// Available reports why this host cannot redirect ports, or nil.
func (m *Manager) Available() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.available()
}

func (m *Manager) available() error {
	if err := m.t.Usable(); err != nil {
		return err
	}
	if _, err := m.t.R.Run("", "list", "tables"); err != nil {
		return ErrNotPermitted
	}
	return nil
}

// Ensure makes the host match redirects: it loads the table when the rules changed or the
// table went missing, and removes it when no redirect is left. Calling it again is free.
func (m *Manager) Ensure(redirects []Redirect, protected []uint16) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(redirects) == 0 {
		if m.applied == "" {
			// One attempt per run at a table an earlier run left; a host that refuses has none.
			if !m.cleaned {
				_ = m.t.Remove()
				m.cleaned = true
			}
			return nil
		}
		if err := m.t.Remove(); err != nil {
			return err
		}
		m.applied, m.cleaned = "", true
		return nil
	}
	rules := Ruleset(redirects, protected)
	if rules == m.applied {
		if m.now().Sub(m.checked) < presenceCheckEvery {
			return nil
		}
		if m.t.Present() {
			m.checked = m.now()
			return nil
		}
	}
	if err := m.available(); err != nil {
		return err
	}
	if err := m.t.Load(rules); err != nil {
		// The transaction is atomic, so an earlier table may still be loaded: look for it again.
		m.applied, m.cleaned = "", false
		return err
	}
	m.applied, m.checked = rules, m.now()
	return nil
}

// Status reports whether the table is loaded and how many flows it has redirected.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.t.State()
	out := Status{Supported: st.Supported, NftInstalled: st.NftInstalled, Active: st.Active, Packets: st.Packets["redirected"]}
	out.Permitted = st.Active || (st.NftInstalled && m.available() == nil)
	return out
}
