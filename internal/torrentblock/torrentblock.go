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
	"strings"
	"sync"
	"time"
)

// TableName is the inet table the panel owns; nothing else is ever touched.
const TableName = "still_torrent_block"

// kernelAWGInterfaces matches every interface amneziawgkern creates (xui-awg<id>).
const kernelAWGInterfaces = "xui-awg*"

// peerTimeout is how long a swarm peer or a tunnel client that sent a signature stays cut.
const peerTimeout = "30m"

// allowedPorts stay reachable for peers and banned tunnel clients: DNS, web, DNS over TLS.
const allowedPorts = "53, 80, 443, 853"

const commandTimeout = 10 * time.Second

var (
	ErrUnsupported = errors.New("nftables filtering needs Linux")
	ErrNftMissing  = errors.New("nft is not installed")
)

var (
	btHandshake    = []byte("\x13BitTorrent prot")
	dhtQuery       = []byte("d1:ad2:id20:")
	dhtResponse    = []byte("d1:rd2:id20:")
	trackerConnect = []byte{0x00, 0x00, 0x04, 0x17, 0x27, 0x10, 0x19, 0x80, 0, 0, 0, 0}
)

// payload renders bytes as an nft payload match on the transport payload start.
func payload(b []byte) (string, string) {
	return fmt.Sprintf("@ih,0,%d", len(b)*8), "0x" + hex.EncodeToString(b)
}

// Ruleset returns one nft transaction that replaces the table whether or not it exists.
func Ruleset() string {
	btAt, bt := payload(btHandshake)
	dhtAt, dhtQ := payload(dhtQuery)
	_, dhtR := payload(dhtResponse)
	trkAt, trk := payload(trackerConnect)
	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\ndelete table inet %s\n", TableName, TableName)
	fmt.Fprintf(&b, "table inet %s {\n", TableName)
	for _, c := range counterNames {
		fmt.Fprintf(&b, "\tcounter %s {}\n", c)
	}
	fmt.Fprintf(&b, "\tset peer4 { type ipv4_addr; flags dynamic,timeout; timeout %s; size 65536; }\n", peerTimeout)
	fmt.Fprintf(&b, "\tset peer6 { type ipv6_addr; flags dynamic,timeout; timeout %s; size 65536; }\n", peerTimeout)
	fmt.Fprintf(&b, "\tset ban4 { type ipv4_addr; flags dynamic,timeout; timeout %s; size 4096; }\n", peerTimeout)
	fmt.Fprintf(&b, "\tset ban6 { type ipv6_addr; flags dynamic,timeout; timeout %s; size 4096; }\n", peerTimeout)

	b.WriteString("\tchain signature {\n")
	fmt.Fprintf(&b, "\t\tmeta l4proto tcp %s %s counter name sig_bt goto hit\n", btAt, bt)
	b.WriteString("\t\tmeta l4proto udp udp dport { 53, 443, 853 } return\n")
	fmt.Fprintf(&b, "\t\tmeta l4proto udp %s { %s, %s } counter name sig_dht goto hit\n", dhtAt, dhtQ, dhtR)
	fmt.Fprintf(&b, "\t\tmeta l4proto udp udp length %d %s %s counter name sig_tracker goto hit\n", 8+len(trackerConnect)+4, trkAt, trk)
	// uTP ST_SYN: type 4 version 1, a known extension, zero timestamp difference (BEP 29).
	b.WriteString("\t\tmeta l4proto udp udp length >= 28 @ih,0,8 0x41 @ih,8,8 < 3 @ih,64,32 0 counter name sig_utp goto hit\n")
	b.WriteString("\t}\n")

	b.WriteString("\tchain hit {\n")
	b.WriteString("\t\tmeta nfproto ipv4 update @peer4 { ip daddr }\n")
	b.WriteString("\t\tmeta nfproto ipv6 update @peer6 { ip6 daddr }\n")
	fmt.Fprintf(&b, "\t\tiifname %q meta nfproto ipv4 update @ban4 { ip saddr }\n", kernelAWGInterfaces)
	fmt.Fprintf(&b, "\t\tiifname %q meta nfproto ipv6 update @ban6 { ip6 saddr }\n", kernelAWGInterfaces)
	b.WriteString("\t\tdrop\n\t}\n")

	b.WriteString("\tchain peers {\n")
	fmt.Fprintf(&b, "\t\tmeta l4proto { tcp, udp } th dport { %s } return\n", allowedPorts)
	b.WriteString("\t\tip daddr @peer4 counter name peer_hit drop\n")
	b.WriteString("\t\tip6 daddr @peer6 counter name peer_hit drop\n")
	b.WriteString("\t}\n")

	// Packets of connections clients open to the server are conntrack replies: never inspected.
	b.WriteString("\tchain out {\n\t\ttype filter hook output priority 0; policy accept;\n")
	b.WriteString("\t\toifname \"lo\" return\n\t\tct direction reply return\n")
	b.WriteString("\t\tjump signature\n\t\tjump peers\n\t}\n")

	b.WriteString("\tchain tunnel {\n")
	b.WriteString("\t\tct direction reply return\n")
	fmt.Fprintf(&b, "\t\tip saddr @ban4 meta l4proto { tcp, udp } th dport != { %s } counter name ban_hit drop\n", allowedPorts)
	fmt.Fprintf(&b, "\t\tip6 saddr @ban6 meta l4proto { tcp, udp } th dport != { %s } counter name ban_hit drop\n", allowedPorts)
	b.WriteString("\t\tjump signature\n\t\tjump peers\n\t}\n")

	b.WriteString("\tchain routed {\n\t\ttype filter hook forward priority 0; policy accept;\n")
	fmt.Fprintf(&b, "\t\tiifname %q jump tunnel\n\t}\n", kernelAWGInterfaces)
	b.WriteString("}\n")
	return b.String()
}

// removal deletes the table and succeeds when it is already gone.
func removal() string {
	return fmt.Sprintf("add table inet %s\ndelete table inet %s\n", TableName, TableName)
}

var counterNames = []string{"sig_bt", "sig_dht", "sig_tracker", "sig_utp", "peer_hit", "ban_hit"}

// Counters are packet counts since the table was last loaded.
type Counters struct {
	Handshakes uint64 `json:"handshakes"`
	DHT        uint64 `json:"dht"`
	Trackers   uint64 `json:"trackers"`
	UTP        uint64 `json:"utp"`
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
func (m *Manager) Apply() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyLocked()
}

func (m *Manager) applyLocked() error {
	if err := m.usable(); err != nil {
		return err
	}
	if out, err := m.r.run(Ruleset(), "-f", "-"); err != nil {
		return fmt.Errorf("nft: %w: %s", err, out)
	}
	return nil
}

// Ensure loads the table only when it is missing, so live peer sets survive.
func (m *Manager) Ensure() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.usable(); err != nil {
		return err
	}
	if m.present() {
		return nil
	}
	return m.applyLocked()
}

// Remove deletes the table; nothing to do on hosts that cannot have one.
func (m *Manager) Remove() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.usable() != nil {
		return nil
	}
	if out, err := m.r.run(removal(), "-f", "-"); err != nil {
		return fmt.Errorf("nft: %w: %s", err, out)
	}
	return nil
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
		case "peer_hit":
			c.PeerDrops = item.Counter.Packets
		case "ban_hit":
			c.TunnelBans = item.Counter.Packets
		}
	}
	return c
}
