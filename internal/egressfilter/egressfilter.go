// Package egressfilter keeps clients off mail spam, private networks and the server's own ports:
// Xray by generated routing rules, AmneziaWG kernel-mode clients by one nftables table.
package egressfilter

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TableName is the inet table the panel owns; nothing else is ever touched.
const TableName = "still_egress_filter"

// clientInterfaces matches every interface amneziawgkern creates (xui-awg<id>).
const clientInterfaces = "xui-awg*"

// DNSPort is never blockable: clients need name resolution through any filter.
const DNSPort = 53

// MaxCustomEntries caps the custom list so a pasted blocklist cannot bloat every config.
const MaxCustomEntries = 256

const commandTimeout = 10 * time.Second

var (
	ErrUnsupported = errors.New("nftables filtering needs Linux")
	ErrNftMissing  = errors.New("nft is not installed")
)

// SMTPPorts is server-to-server mail: what spam from a VPN client uses.
var SMTPPorts = []PortRange{{25, 25}}

// SubmissionPorts is authenticated mail submission (SMTPS, submission).
var SubmissionPorts = []PortRange{{465, 465}, {587, 587}}

// PrivateV4 and PrivateV6 are the private, loopback, link-local, multicast and reserved ranges no
// client needs. NAT64 prefixes are left out: a NAT64 host reaches the IPv4 internet through them.
var (
	PrivateV4 = []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"224.0.0.0/4", "240.0.0.0/4",
	}
	PrivateV6 = []string{"::/127", "fc00::/7", "fe80::/10", "ff00::/8"}
)

// Filter is what an operator chooses; the zero value blocks nothing.
type Filter struct {
	// SMTP blocks port 25.
	SMTP bool `json:"smtp"`
	// MailSubmission blocks ports 465 and 587.
	MailSubmission bool `json:"mailSubmission"`
	// Private blocks private, loopback, link-local and similar destinations.
	Private bool `json:"private"`
	// Server blocks this server's loopback services and its panel and SSH ports.
	Server bool `json:"server"`
	// Custom entries are addresses or CIDRs (blocked on every port) and ports or port
	// ranges (blocked on every address), e.g. "203.0.113.0/24", "6667", "1000-2000".
	Custom []string `json:"custom"`
}

// PortRange is an inclusive range; a single port has From == To.
type PortRange struct {
	From uint16
	To   uint16
}

func (r PortRange) String() string {
	if r.From == r.To {
		return strconv.Itoa(int(r.From))
	}
	return strconv.Itoa(int(r.From)) + "-" + strconv.Itoa(int(r.To))
}

// Custom is the parsed custom list.
type Custom struct {
	V4    []netip.Prefix
	V6    []netip.Prefix
	Ports []PortRange
}

// Any reports whether the filter blocks anything at all.
func (f Filter) Any() bool {
	return f.SMTP || f.MailSubmission || f.Private || f.Server || len(f.Custom) > 0
}

// MailPorts are the mail ports the filter blocks.
func (f Filter) MailPorts() []PortRange {
	var ports []PortRange
	if f.SMTP {
		ports = append(ports, SMTPPorts...)
	}
	if f.MailSubmission {
		ports = append(ports, SubmissionPorts...)
	}
	return ports
}

// Normalize validates the custom list and returns the filter with it in canonical
// form (masked prefixes, sorted, deduplicated), so equal filters compare equal.
func (f Filter) Normalize() (Filter, error) {
	c, err := ParseCustom(f.Custom)
	if err != nil {
		return Filter{}, err
	}
	f.Custom = c.Strings()
	return f, nil
}

// Fingerprint identifies a normalized filter, e.g. to tell whether a node already has it.
func (f Filter) Fingerprint() string {
	raw, _ := json.Marshal(f)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// ParseCustom accepts entries separated by commas, spaces or new lines.
func ParseCustom(entries []string) (Custom, error) {
	var c Custom
	n := 0
	for _, entry := range entries {
		for _, tok := range strings.FieldsFunc(entry, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
		}) {
			n++
			if n > MaxCustomEntries {
				return Custom{}, fmt.Errorf("custom list: more than %d entries", MaxCustomEntries)
			}
			if err := c.add(tok); err != nil {
				return Custom{}, err
			}
		}
	}
	c.V4 = compactPrefixes(c.V4)
	c.V6 = compactPrefixes(c.V6)
	slices.SortFunc(c.Ports, func(a, b PortRange) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
	})
	c.Ports = mergePorts(c.Ports)
	return c, nil
}

// mergePorts joins overlapping and adjacent ranges of a sorted list, so the
// nftables interval set never holds conflicting elements.
func mergePorts(in []PortRange) []PortRange {
	out := in[:0]
	for _, r := range in {
		if n := len(out); n > 0 && int(r.From) <= int(out[n-1].To)+1 {
			out[n-1].To = max(out[n-1].To, r.To)
			continue
		}
		out = append(out, r)
	}
	return out
}

func (c *Custom) add(tok string) error {
	if r, ok, err := parsePortRange(tok); ok || err != nil {
		if err != nil {
			return err
		}
		if r.From <= DNSPort && DNSPort <= r.To {
			return fmt.Errorf("custom list: %q covers DNS port 53, which clients need", tok)
		}
		c.Ports = append(c.Ports, r)
		return nil
	}
	var p netip.Prefix
	if strings.Contains(tok, "/") {
		parsed, err := netip.ParsePrefix(tok)
		if err != nil {
			return fmt.Errorf("custom list: %q is not an address, CIDR, port or port range", tok)
		}
		p = parsed.Masked()
	} else {
		a, err := netip.ParseAddr(tok)
		if err != nil || a.Zone() != "" {
			return fmt.Errorf("custom list: %q is not an address, CIDR, port or port range", tok)
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if p.Addr().Is4In6() {
		p = netip.PrefixFrom(p.Addr().Unmap(), max(p.Bits()-96, 0)).Masked()
	}
	if p.Bits() == 0 {
		return fmt.Errorf("custom list: %q would block every destination", tok)
	}
	if p.Addr().Is4() {
		c.V4 = append(c.V4, p)
	} else {
		c.V6 = append(c.V6, p)
	}
	return nil
}

func parsePortRange(tok string) (PortRange, bool, error) {
	from, to, isRange := strings.Cut(tok, "-")
	if !isDigits(from) || (isRange && !isDigits(to)) {
		return PortRange{}, false, nil
	}
	if !isRange {
		to = from
	}
	a, errA := strconv.Atoi(from)
	b, errB := strconv.Atoi(to)
	if errA != nil || errB != nil || a < 1 || b > 65535 || a > b {
		return PortRange{}, true, fmt.Errorf("custom list: %q is not a port (1-65535) or a range like 1000-2000", tok)
	}
	return PortRange{uint16(a), uint16(b)}, true, nil
}

func isDigits(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// compactPrefixes sorts and drops prefixes covered by another one, so the nftables
// interval sets never hold overlapping elements.
func compactPrefixes(in []netip.Prefix) []netip.Prefix {
	slices.SortFunc(in, func(a, b netip.Prefix) int {
		return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
	})
	out := in[:0]
	for _, p := range in {
		if len(out) > 0 && out[len(out)-1].Overlaps(p) && out[len(out)-1].Bits() <= p.Bits() {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Strings renders the parsed list back into canonical entries.
func (c Custom) Strings() []string {
	out := make([]string, 0, len(c.V4)+len(c.V6)+len(c.Ports))
	for _, p := range append(slices.Clone(c.V4), c.V6...) {
		out = append(out, prefixString(p))
	}
	for _, r := range c.Ports {
		out = append(out, r.String())
	}
	return out
}

// prefixString prints a single address without its /32 or /128.
func prefixString(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

// PortList renders ranges in Xray's "25,465,1000-2000" form.
func PortList(ranges []PortRange) string {
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		parts = append(parts, r.String())
	}
	return strings.Join(parts, ",")
}

// Spec is everything the nftables table is rendered from.
type Spec struct {
	Filter Filter
	// ServerPorts are this server's own TCP management ports (panel, SSH).
	ServerPorts []uint16
}

func (s Spec) serverPorts() []uint16 {
	ports := slices.Clone(s.ServerPorts)
	slices.Sort(ports)
	ports = slices.Compact(ports)
	return slices.DeleteFunc(ports, func(p uint16) bool { return p == 0 || p == DNSPort })
}

// Needed reports whether the spec blocks anything for kernel-forwarded clients.
func (s Spec) Needed() bool {
	f := s.Filter
	return f.SMTP || f.MailSubmission || f.Private || len(f.Custom) > 0 || (f.Server && len(s.serverPorts()) > 0)
}

func portElements(ranges []PortRange) string {
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		parts = append(parts, r.String())
	}
	return strings.Join(parts, ", ")
}

func prefixElements(prefixes []netip.Prefix) string {
	parts := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, ", ")
}

var counterNames = []string{"mail", "private", "server", "custom"}

// Ruleset returns one nft transaction replacing the table; it judges only connections AmneziaWG
// kernel clients open, never the server's own traffic. An unparsable custom list is an error.
func Ruleset(spec Spec) (string, error) {
	custom, err := ParseCustom(spec.Filter.Custom)
	if err != nil {
		return "", err
	}
	f := spec.Filter
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("add table inet %s\ndelete table inet %s", TableName, TableName)
	w("table inet %s {", TableName)
	for _, c := range counterNames {
		w("\tcounter %s {}", c)
	}
	mail := f.MailPorts()
	if len(mail) > 0 {
		w("\tset mail_ports { type inet_service; flags interval; elements = { %s }; }", portElements(mail))
	}
	if f.Private {
		w("\tset private4 { type ipv4_addr; flags interval; elements = { %s }; }", strings.Join(PrivateV4, ", "))
		w("\tset private6 { type ipv6_addr; flags interval; elements = { %s }; }", strings.Join(PrivateV6, ", "))
	}
	server := spec.serverPorts()
	if f.Server && len(server) > 0 {
		parts := make([]string, 0, len(server))
		for _, p := range server {
			parts = append(parts, strconv.Itoa(int(p)))
		}
		w("\tset server_ports { type inet_service; elements = { %s }; }", strings.Join(parts, ", "))
	}
	if len(custom.V4) > 0 {
		w("\tset custom4 { type ipv4_addr; flags interval; elements = { %s }; }", prefixElements(custom.V4))
	}
	if len(custom.V6) > 0 {
		w("\tset custom6 { type ipv6_addr; flags interval; elements = { %s }; }", prefixElements(custom.V6))
	}
	if len(custom.Ports) > 0 {
		w("\tset custom_ports { type inet_service; flags interval; elements = { %s }; }", portElements(custom.Ports))
	}

	// A refused connection fails at once instead of hanging until the client times out.
	w("\tchain deny {")
	w("\t\tmeta l4proto tcp reject with tcp reset")
	w("\t\treject with icmpx type admin-prohibited")
	w("\t}")

	w("\tchain client {")
	// Name resolution always goes through, even to a resolver on a private network.
	w("\t\tmeta l4proto { tcp, udp } th dport %d return", DNSPort)
	if len(mail) > 0 {
		w("\t\tmeta l4proto tcp tcp dport @mail_ports counter name mail goto deny")
	}
	if f.Private {
		// Peer-to-peer inside the tunnel is the tunnel's own business, not egress.
		w("\t\toifname != %q ip daddr @private4 counter name private goto deny", clientInterfaces)
		w("\t\toifname != %q ip6 daddr @private6 counter name private goto deny", clientInterfaces)
	}
	if len(custom.V4) > 0 {
		w("\t\tip daddr @custom4 counter name custom goto deny")
	}
	if len(custom.V6) > 0 {
		w("\t\tip6 daddr @custom6 counter name custom goto deny")
	}
	if len(custom.Ports) > 0 {
		w("\t\tmeta l4proto { tcp, udp } th dport @custom_ports counter name custom goto deny")
	}
	w("\t}")

	// Only the original direction of connections a tunnel client opened; replies and
	// connections forwarded to a client (port forwarding) are never judged.
	w("\tchain routed {\n\t\ttype filter hook forward priority 0; policy accept;")
	w("\t\tiifname %q ct direction original jump client\n\t}", clientInterfaces)
	if f.Server && len(server) > 0 {
		// A client reaching the server itself: only the listed TCP ports, so DNS on the
		// tunnel address and every other service keep working.
		w("\tchain local {\n\t\ttype filter hook input priority 0; policy accept;")
		w("\t\tiifname %q ct direction original meta l4proto tcp tcp dport @server_ports counter name server goto deny\n\t}", clientInterfaces)
	}
	w("}")
	return b.String(), nil
}

// removal deletes the table and succeeds when it is already gone.
func removal() string {
	return fmt.Sprintf("add table inet %s\ndelete table inet %s\n", TableName, TableName)
}

// Counters are packet counts since the table was last loaded.
type Counters struct {
	Mail    uint64 `json:"mail"`
	Private uint64 `json:"private"`
	Server  uint64 `json:"server"`
	Custom  uint64 `json:"custom"`
}

// Status describes the host side of the filter.
type Status struct {
	Supported    bool     `json:"supported"`
	NftInstalled bool     `json:"nftInstalled"`
	Active       bool     `json:"active"`
	Counters     Counters `json:"counters"`
}

// runner abstracts the host so the manager is testable off Linux.
type runner interface {
	lookPath(name string) error
	run(stdin string, args ...string) (string, error)
}

type osRunner struct{}

func (osRunner) lookPath(name string) error {
	_, err := exec.LookPath(name)
	return err
}

func (osRunner) run(stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nft", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Manager applies and removes the table; safe for concurrent use.
type Manager struct {
	mu   sync.Mutex
	r    runner
	goos string
	// loaded is the ruleset this process last loaded, so Ensure only reloads on change.
	loaded string
}

var defaultManager = &Manager{r: osRunner{}, goos: runtime.GOOS}

// Default is the manager for the real host.
func Default() *Manager { return defaultManager }

func (m *Manager) usable() error {
	if m.goos != "linux" {
		return ErrUnsupported
	}
	if m.r.lookPath("nft") != nil {
		return ErrNftMissing
	}
	return nil
}

func (m *Manager) nft(tx string) error {
	if out, err := m.r.run(tx, "-f", "-"); err != nil {
		return fmt.Errorf("nft: %w: %s", err, out)
	}
	return nil
}

// Apply loads the table for spec, replacing any previous copy, or removes it when the
// spec blocks nothing for kernel-forwarded clients.
func (m *Manager) Apply(spec Spec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyLocked(spec)
}

func (m *Manager) applyLocked(spec Spec) error {
	if err := m.usable(); err != nil {
		return err
	}
	if !spec.Needed() {
		m.loaded = ""
		return m.nft(removal())
	}
	tx, err := Ruleset(spec)
	if err != nil {
		return err
	}
	if err := m.nft(tx); err != nil {
		return err
	}
	m.loaded = tx
	return nil
}

// Ensure reloads the table after a reboot or a foreign ruleset flush, or when the spec
// changed; an unchanged loaded table is left alone so its counters keep counting.
func (m *Manager) Ensure(spec Spec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.usable(); err != nil {
		return err
	}
	if spec.Needed() {
		tx, err := Ruleset(spec)
		if err != nil {
			return err
		}
		if tx == m.loaded && m.present() {
			return nil
		}
	}
	return m.applyLocked(spec)
}

// Remove deletes the table; nothing to do on hosts that cannot have one.
func (m *Manager) Remove() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loaded = ""
	if m.usable() != nil {
		return nil
	}
	return m.nft(removal())
}

func (m *Manager) present() bool {
	_, err := m.r.run("", "list", "table", "inet", TableName)
	return err == nil
}

// Status reports whether the table is loaded and what it has refused.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Supported: m.goos == "linux"}
	if !st.Supported {
		return st
	}
	st.NftInstalled = m.r.lookPath("nft") == nil
	if !st.NftInstalled {
		return st
	}
	out, err := m.r.run("", "-j", "list", "counters", "table", "inet", TableName)
	if err != nil {
		return st
	}
	st.Active = true
	st.Counters = parseCounters(out)
	return st
}

func parseCounters(out string) Counters {
	var doc struct {
		Nftables []struct {
			Counter *struct {
				Name    string `json:"name"`
				Table   string `json:"table"`
				Packets uint64 `json:"packets"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	var c Counters
	if json.Unmarshal([]byte(out), &doc) != nil {
		return c
	}
	for _, item := range doc.Nftables {
		if item.Counter == nil || item.Counter.Table != TableName {
			continue
		}
		switch item.Counter.Name {
		case "mail":
			c.Mail = item.Counter.Packets
		case "private":
			c.Private = item.Counter.Packets
		case "server":
			c.Server = item.Counter.Packets
		case "custom":
			c.Custom = item.Counter.Packets
		}
	}
	return c
}
