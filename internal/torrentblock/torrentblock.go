// Package torrentblock keeps BitTorrent off the server's uplink with one nftables table:
// signatures on egress the server originates, plus forwarded AmneziaWG kernel-mode traffic.
package torrentblock

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TableName is the inet table the panel owns; nothing else is ever touched.
const TableName = "still_torrent_block"

// kernelAWGInterfaces matches every interface amneziawgkern creates (xui-awg<id>).
const kernelAWGInterfaces = "xui-awg*"

// peerTimeout is how long a learned peer endpoint or a tunnel client that sent a signature stays cut.
const peerTimeout = "30m"

// serviceSet is never learned or cut: DNS, web, DNS over TLS.
const serviceSet = "{ 53, 80, 443, 853 }"

// learnRate caps how many endpoints one remote address can have learned per minute.
const learnRate = "30/minute"

// defaultProtected is always in the protected set (SSH), whatever the caller passes.
var defaultProtected = []uint16{22}

const commandTimeout = 10 * time.Second

var (
	ErrUnsupported = errors.New("nftables filtering needs Linux")
	ErrNftMissing  = errors.New("nft is not installed")
)

var (
	btHandshake    = []byte("\x13BitTorrent prot")
	btReply        = []byte("\x13BitTorrent protocol")
	dhtQuery       = []byte("d1:ad2:id20:")
	dhtResponse    = []byte("d1:rd2:id20:")
	trackerConnect = []byte{0x00, 0x00, 0x04, 0x17, 0x27, 0x10, 0x19, 0x80, 0, 0, 0, 0}
)

// payload renders bytes as nft matches on the transport payload start, in the
// 16-byte pieces that are the most one payload expression can compare.
func payload(b []byte) string {
	parts := make([]string, 0, 2)
	for off := 0; off < len(b); off += 16 {
		chunk := b[off:min(off+16, len(b))]
		parts = append(parts, fmt.Sprintf("@ih,%d,%d 0x%s", off*8, len(chunk)*8, hex.EncodeToString(chunk)))
	}
	return strings.Join(parts, " ")
}

func portList(protected []uint16) []uint16 {
	ports := append(slices.Clone(defaultProtected), protected...)
	slices.Sort(ports)
	ports = slices.Compact(ports)
	return slices.DeleteFunc(ports, func(p uint16) bool { return p == 0 })
}

func portElements(protected []uint16) string {
	parts := make([]string, 0, len(protected))
	for _, p := range portList(protected) {
		parts = append(parts, strconv.Itoa(int(p)))
	}
	return strings.Join(parts, ", ")
}

// Ruleset returns one nft transaction that replaces the table. Outbound signatures are only
// dropped; a peer is learned solely from its own BitTorrent answer, so no third party gets cut.
func Ruleset(protected []uint16) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("add table inet %s\ndelete table inet %s", TableName, TableName)
	w("table inet %s {", TableName)
	for _, c := range counterNames {
		w("\tcounter %s {}", c)
	}
	w("\tset protected { type inet_service; elements = { %s }; }", portElements(protected))
	w("\tset peer4 { type ipv4_addr . inet_service; flags dynamic,timeout; timeout %s; size 16384; }", peerTimeout)
	w("\tset peer6 { type ipv6_addr . inet_service; flags dynamic,timeout; timeout %s; size 16384; }", peerTimeout)
	w("\tset learn4 { type ipv4_addr; flags dynamic,timeout; timeout 1m; size 16384; }")
	w("\tset learn6 { type ipv6_addr; flags dynamic,timeout; timeout 1m; size 16384; }")
	w("\tset ban4 { type ipv4_addr; flags dynamic,timeout; timeout %s; size 4096; }", peerTimeout)
	w("\tset ban6 { type ipv6_addr; flags dynamic,timeout; timeout %s; size 4096; }", peerTimeout)

	w("\tchain signature {")
	w("\t\tmeta l4proto tcp %s counter name sig_bt goto hit", payload(btHandshake))
	w("\t\tmeta l4proto udp udp dport { 53, 443, 853 } return")
	w("\t\tmeta l4proto udp @ih,0,96 { 0x%s, 0x%s } counter name sig_dht goto hit", hex.EncodeToString(dhtQuery), hex.EncodeToString(dhtResponse))
	w("\t\tmeta l4proto udp udp length %d %s counter name sig_tracker goto hit", 8+len(trackerConnect)+4, payload(trackerConnect))
	// uTP ST_SYN: type 4 version 1, a known extension, zero timestamp difference (BEP 29).
	w("\t\tmeta l4proto udp udp length >= 28 @ih,0,8 0x41 @ih,8,8 < 3 @ih,64,32 0 counter name sig_utp goto hit")
	w("\t}")
	// A tunnel client can only ban its own source address.
	w("\tchain hit {")
	w("\t\tiifname %q meta nfproto ipv4 update @ban4 { ip saddr }", kernelAWGInterfaces)
	w("\t\tiifname %q meta nfproto ipv6 update @ban6 { ip6 saddr }", kernelAWGInterfaces)
	w("\t\tdrop\n\t}")

	w("\tchain peers {")
	w("\t\tmeta l4proto { tcp, udp } th dport %s return", serviceSet)
	w("\t\tmeta l4proto { tcp, udp } ip daddr . th dport @peer4 counter name peer_hit drop")
	w("\t\tmeta l4proto { tcp, udp } ip6 daddr . th dport @peer6 counter name peer_hit drop")
	w("\t}")

	// Replies from the remote side: handshake, DHT response, uTP ST_STATE.
	w("\tchain learn {")
	w("\t\tmeta l4proto { tcp, udp } th sport %s return", serviceSet)
	w("\t\tmeta l4proto { tcp, udp } th sport @protected return")
	w("\t\tfib saddr type local return")
	w("\t\tmeta l4proto tcp %s goto learn_peer", payload(btReply))
	w("\t\tmeta l4proto udp %s goto learn_peer", payload(dhtResponse))
	w("\t\tmeta l4proto udp udp length >= 28 @ih,0,8 0x21 @ih,8,8 < 3 goto learn_peer")
	w("\t}")
	w("\tchain learn_peer {")
	w("\t\tmeta nfproto ipv4 update @learn4 { ip saddr limit rate over %s } drop", learnRate)
	w("\t\tmeta nfproto ipv6 update @learn6 { ip6 saddr limit rate over %s } drop", learnRate)
	w("\t\tmeta nfproto ipv4 update @peer4 { ip saddr . th sport }")
	w("\t\tmeta nfproto ipv6 update @peer6 { ip6 saddr . th sport }")
	w("\t\tcounter name learned drop")
	w("\t}")

	// The server's answers on connections clients opened to it are never checked.
	w("\tchain out {\n\t\ttype filter hook output priority 0; policy accept;")
	w("\t\toifname \"lo\" return\n\t\tct direction reply return")
	w("\t\tjump signature\n\t\tjump peers\n\t}")
	w("\tchain in {\n\t\ttype filter hook input priority 0; policy accept;")
	w("\t\tiifname \"lo\" return\n\t\tct direction reply jump learn\n\t}")

	w("\tchain tunnel {")
	w("\t\tip saddr @ban4 meta l4proto { tcp, udp } th dport != %s counter name ban_hit drop", serviceSet)
	w("\t\tip6 saddr @ban6 meta l4proto { tcp, udp } th dport != %s counter name ban_hit drop", serviceSet)
	w("\t\tjump signature\n\t\tjump peers\n\t}")
	w("\tchain routed {\n\t\ttype filter hook forward priority 0; policy accept;")
	w("\t\tiifname %q ct direction original jump tunnel", kernelAWGInterfaces)
	w("\t\toifname %q ct direction reply jump learn\n\t}", kernelAWGInterfaces)
	w("}")
	return b.String()
}

// protectedRefresh swaps the protected ports of a loaded table, keeping its sets.
func protectedRefresh(protected []uint16) string {
	return fmt.Sprintf("flush set inet %s protected\nadd element inet %s protected { %s }\n",
		TableName, TableName, portElements(protected))
}

// removal deletes the table and succeeds when it is already gone.
func removal() string {
	return fmt.Sprintf("add table inet %s\ndelete table inet %s\n", TableName, TableName)
}

var counterNames = []string{"sig_bt", "sig_dht", "sig_tracker", "sig_utp", "learned", "peer_hit", "ban_hit"}

// Counters are packet counts since the table was last loaded.
type Counters struct {
	Handshakes uint64 `json:"handshakes"`
	DHT        uint64 `json:"dht"`
	Trackers   uint64 `json:"trackers"`
	UTP        uint64 `json:"utp"`
	Learned    uint64 `json:"learned"`
	PeerDrops  uint64 `json:"peerDrops"`
	TunnelBans uint64 `json:"tunnelBans"`
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

// Apply loads the table, replacing any previous copy (its counters and sets restart).
// protected are the server's own service ports: never learned as a peer endpoint.
func (m *Manager) Apply(protected []uint16) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyLocked(protected)
}

func (m *Manager) applyLocked(protected []uint16) error {
	if err := m.usable(); err != nil {
		return err
	}
	return m.nft(Ruleset(protected))
}

func (m *Manager) nft(tx string) error {
	if out, err := m.r.run(tx, "-f", "-"); err != nil {
		return fmt.Errorf("nft: %w: %s", err, out)
	}
	return nil
}

// Ensure loads a missing table, or only refreshes the protected ports of a loaded one
// so learned peers and bans survive.
func (m *Manager) Ensure(protected []uint16) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.usable(); err != nil {
		return err
	}
	if m.present() {
		return m.nft(protectedRefresh(protected))
	}
	return m.applyLocked(protected)
}

// Remove deletes the table; nothing to do on hosts that cannot have one.
func (m *Manager) Remove() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.usable() != nil {
		return nil
	}
	return m.nft(removal())
}

func (m *Manager) present() bool {
	_, err := m.r.run("", "list", "table", "inet", TableName)
	return err == nil
}

// Status reports whether the table is loaded and what it has caught.
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
		case "sig_bt":
			c.Handshakes = item.Counter.Packets
		case "sig_dht":
			c.DHT = item.Counter.Packets
		case "sig_tracker":
			c.Trackers = item.Counter.Packets
		case "sig_utp":
			c.UTP = item.Counter.Packets
		case "learned":
			c.Learned = item.Counter.Packets
		case "peer_hit":
			c.PeerDrops = item.Counter.Packets
		case "ban_hit":
			c.TunnelBans = item.Counter.Packets
		}
	}
	return c
}
