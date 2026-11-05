package amneziawgnet

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
)

// DeviceOptions carries AmneziaWG 3.0's device-wide fields (header
// protection, content padding, and the five session-timing knobs) --
// mirrored from amneziawg.Instance's identically named fields by every
// caller (see the 3 Desired{} call sites), not read from Instance
// directly, since amneziawgnet has no dependency on internal/amneziawg
// beyond the plain data types it already imports. Zero-value DeviceOptions
// means amneziawg-go's own real-protocol defaults throughout: classic
// (non-3.0) obfuscation, and its built-in session timings (120s/5s/180s/
// 10s/18 attempts -- device/constants.go).
type DeviceOptions struct {
	// HeaderProtectionKey is a base64 32-byte key. Empty disables AWG 3.0
	// header protection entirely. Non-empty requires every one of
	// Obfuscation31.S1-S4 to be >= 12 (amneziawg-go's own HeaderCipherNonceSize
	// requirement) -- IpcSet will reject the config otherwise.
	HeaderProtectionKey string
	// ContentPaddingAddition, RekeyAfterTime, RekeyTimeout, RejectAfterTime,
	// KeepaliveTimeout, and MaxHandshakeAttempts are each a "low-high" range
	// (or a bare integer), amneziawg-go's own UintRange.FromString grammar
	// (confirmed directly against v3.0.3's device/uapi.go -- all six share
	// the identical parser). Empty leaves that one field at amneziawg-go's
	// own default (sent as an explicit "0", so clearing a field also resets a
	// running device).
	ContentPaddingAddition string
	RekeyAfterTime         string
	RekeyTimeout           string
	RejectAfterTime        string
	KeepaliveTimeout       string
	MaxHandshakeAttempts   string
	// RandomTrailers and DisableCookies are AmneziaWG 3.1's two device-wide
	// bool toggles (confirmed against amneziawg-go v3.1.20260814's
	// device/uapi.go: "random_trailers"/"disable_cookies", both
	// strconv.ParseBool). Unlike the string fields above, buildUAPIConfig
	// emits these unconditionally on every call -- a bool has no "absent"
	// value to gate on, and always emitting both means the reconfigure-
	// in-place diff correctly notices a true->false edit, not just
	// false->true. RandomTrailers requires the peer to also run AmneziaWG
	// 3.1+ with it enabled: amneziawg-go's own receive path only accepts
	// an oversized (trailer-padded) packet when the RECEIVING side's own
	// RandomTrailers is also true, so a one-sided setting makes that
	// side's packets start getting silently dropped by the other.
	// DisableCookies is purely local (no peer-side coordination needed)
	// but trades away WireGuard's handshake-flood DoS-protection cookie
	// replies for a less distinctive packet shape during a flood.
	RandomTrailers bool
	DisableCookies bool
	// Logger is passed to device.NewDevice as-is; nil uses a silent logger
	// (device.NewLogger(device.LogLevelSilent, "")).
	Logger *device.Logger
}

// Device is one running embedded AmneziaWG interface: an amneziawg-go
// Device over a gVisor netstack, plus the raw *stack.Stack a caller needs to
// attach a TCP/UDP forwarder (see forwarder.go / udp.go). Closing it tears
// down both the WireGuard device and the underlying tun/stack.
type Device struct {
	*device.Device
	Stack *stack.Stack

	// localAddrs snapshots the netstack interface addresses (gVisor exposes
	// no read-back); set once at construction, read-only afterwards.
	localAddrs []netip.Addr
}

// LocalAddresses returns the configured tunnel-local address(es).
func (d *Device) LocalAddresses() []netip.Addr { return d.localAddrs }

// NewDevice constructs, configures, and brings up an embedded AmneziaWG
// interface for inst in one call: a gVisor-backed tun.Device sized to
// amneziawg.EffectiveMTU, addressed with inst.Address, configured via
// UAPI with inst.Obfuscation, inst.PrivateKey, opts' AWG 3.0 fields, and one
// UAPI peer per inst.Peers entry. It does not attach a forwarder or start
// relaying traffic -- that's the caller's job (see AttachTCPForwarder /
// AttachUDPHandler) -- which is exactly why a caller that will relay real
// traffic must NOT use this function: see newUnconfiguredDevice's doc
// comment for why, and use newUnconfiguredDevice + Configure instead.
func NewDevice(inst amneziawg.Instance, opts DeviceOptions) (*Device, error) {
	dev, err := newUnconfiguredDevice(inst, opts)
	if err != nil {
		return nil, err
	}
	if err := dev.Configure(inst, opts); err != nil {
		return nil, err
	}
	return dev, nil
}

// newUnconfiguredDevice builds the tun/netstack/device trio but does not
// configure any peers or bring the interface up -- a caller that will relay
// real traffic MUST attach its TCP/UDP handlers (AttachTCPForwarder /
// AttachUDPHandler) against the returned Device.Stack BEFORE calling
// Configure, not after.
//
// This ordering is not a style preference: Configure's IpcSet is what
// starts each configured peer's receive goroutine (amneziawg-go's
// Peer.Start, called from handlePostConfig), and a peer whose handshake
// completes fast enough (e.g. an already-connected client reconnecting
// right as an MTU/address change forces this package's own Manager to
// rebuild the Device) can begin delivering packets into the stack
// immediately -- concurrently with a caller that only calls
// gstack.SetTransportProtocolHandler (AttachTCPForwarder/AttachUDPHandler)
// after Configure returns. A -race CI run caught exactly this as a real
// WARNING: DATA RACE between stack.(*nic).DeliverTransportPacket (the
// peer's receive goroutine, reading the handler table) and
// stack.(*Stack).SetTransportProtocolHandler (the attaching goroutine,
// writing it). See manager.go's ensureLocked rebuild branch for the real
// call order this function exists to support.
func newUnconfiguredDevice(inst amneziawg.Instance, opts DeviceOptions) (*Device, error) {
	addrs, err := hostAddresses(inst.Address)
	if err != nil {
		return nil, fmt.Errorf("amneziawgnet: %w", err)
	}

	mtu := amneziawg.EffectiveMTU(inst.MTU, inst.Obfuscation.S4)

	tun, gstack, err := createNetTUNWithStack(addrs, mtu)
	if err != nil {
		return nil, fmt.Errorf("amneziawgnet: create netstack: %w", err)
	}

	logger := opts.Logger
	if logger == nil {
		logger = device.NewLogger(device.LogLevelSilent, "")
	}
	bind := newResolvingBind(inst.Listen)
	dev := device.NewDevice(tun, bind, logger)

	return &Device{Device: dev, Stack: gstack, localAddrs: addrs}, nil
}

// Configure applies inst/opts to d via UAPI and brings the interface up.
// Call at most once per Device, and -- for any caller relaying real
// traffic -- only after any AttachTCPForwarder/AttachUDPHandler
// registration against d.Stack (see newUnconfiguredDevice's doc comment
// for why the order matters). Closes d and returns an error if either step
// fails; the caller owns closing anything else it already built against
// d.Stack in that case (e.g. a UDP relay or port-forward set).
func (d *Device) Configure(inst amneziawg.Instance, opts DeviceOptions) error {
	conf, err := buildUAPIConfig(inst, opts)
	if err != nil {
		d.Close()
		return fmt.Errorf("amneziawgnet: %w", err)
	}
	if err := d.IpcSet(conf); err != nil {
		d.Close()
		return fmt.Errorf("amneziawgnet: IpcSet for inbound %d: %w", inst.Id, err)
	}
	if err := d.Up(); err != nil {
		d.Close()
		return fmt.Errorf("amneziawgnet: bring up inbound %d: %w", inst.Id, err)
	}
	return nil
}

// hostAddresses parses each of inst.Address's CIDR strings (e.g.
// "10.8.1.1/24") down to the bare host address the netstack's NIC gets
// configured with -- the interface's own address, not the subnet it routes.
func hostAddresses(addresses []string) ([]netip.Addr, error) {
	out := make([]netip.Addr, 0, len(addresses))
	for _, a := range addresses {
		prefix, err := netip.ParsePrefix(a)
		if err != nil {
			return nil, fmt.Errorf("invalid interface address %q: %w", a, err)
		}
		out = append(out, prefix.Addr())
	}
	return out, nil
}

// buildUAPIConfig renders inst (plus opts' AWG 3.0 fields) as a WireGuard
// UAPI "set" configuration string -- private_key/listen_port/jc.../s1-s4/
// h1-h4/i1-i5 device lines, the AWG 3.0/3.1 device lines (all of them, blanks
// as explicit defaults -- see writeDeviceObfuscationLines), then one public_key/preshared_key/allowed_ip block per peer. Field names
// and format match amneziawg-go v3.0.3's device/uapi.go exactly (confirmed
// against its real source during Phase 0 spiking, not just its docs).
func buildUAPIConfig(inst amneziawg.Instance, opts DeviceOptions) (string, error) {
	var b strings.Builder

	privHex, err := wireguard.KeyToHex(inst.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("invalid server private key: %w", err)
	}
	fmt.Fprintf(&b, "private_key=%s\n", privHex)
	fmt.Fprintf(&b, "listen_port=%d\n", inst.ListenPort)
	// replace_peers makes every apply a full resync (matches this package's
	// own Manager.Ensure semantics): peers no longer in inst.Peers are
	// dropped instead of lingering from a previous IpcSet call.
	b.WriteString("replace_peers=true\n")

	// Rows saved before these bounds existed would allocate without limit per handshake.
	if err := amneziawg.ValidateHandshakePreamble(inst.Obfuscation); err != nil {
		return "", err
	}
	if err := writeDeviceObfuscationLines(&b, inst.Obfuscation, opts); err != nil {
		return "", err
	}

	for _, p := range inst.Peers {
		// A row saved before keys were validated is skipped, not fatal: an
		// error here kept every other client's change off the device.
		pubHex, err := wireguard.KeyToHex(p.PublicKey)
		if err != nil || pubHex == "" {
			continue
		}
		pskHex, err := wireguard.KeyToHex(p.PresharedKey)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "public_key=%s\n", pubHex)
		if pskHex != "" {
			fmt.Fprintf(&b, "preshared_key=%s\n", pskHex)
		}
		for _, allowedIP := range p.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", allowedIP)
		}
	}

	return b.String(), nil
}

// writeDeviceObfuscationLines renders the device-wide obfuscation, AWG 3.0
// and 3.1 lines shared by the inbound and outbound builders. Every line is
// written on every call: amneziawg-go starts an IpcSet from the device's
// current values, so an omitted line means "keep the old value", not "use the
// default". A field cleared in the panel therefore has to be sent as its
// explicit default, or an in-place reconfigure keeps the stale value that a
// freshly built device (and every newly downloaded client config) no longer has.
// The one exception is I1-I5 on a running Device: they are dropped from
// in-place configs, and an edit to them rebuilds (signaturePacketsFingerprint).
func writeDeviceObfuscationLines(b *strings.Builder, o amneziawg.Obfuscation31, opts DeviceOptions) error {
	fmt.Fprintf(b, "jc=%d\njmin=%d\njmax=%d\n", o.Jc, o.Jmin, o.Jmax)
	fmt.Fprintf(b, "s1=%d\ns2=%d\ns3=%d\ns4=%d\n", o.S1, o.S2, o.S3, o.S4)
	// A blank Hn is WireGuard's own message type n, amneziawg-go's default.
	for i, h := range []string{o.H1, o.H2, o.H3, o.H4} {
		writeLineOrDefault(b, fmt.Sprintf("h%d", i+1), h, strconv.Itoa(i+1))
	}
	// An empty "iN=" clears that signature packet (newObfChain("") is nil).
	for i, spec := range []string{o.I1, o.I2, o.I3, o.I4, o.I5} {
		fmt.Fprintf(b, "i%d=%s\n", i+1, spec)
	}

	// A cleared key can only reach a live device as the all-zero one that
	// disables the feature.
	hpHex := strings.Repeat("0", 64)
	if opts.HeaderProtectionKey != "" {
		var err error
		hpHex, err = wireguard.KeyToHex(opts.HeaderProtectionKey)
		if err != nil {
			return fmt.Errorf("invalid header protection key: %w", err)
		}
	}
	fmt.Fprintf(b, "header_protection_key=%s\n", hpHex)
	// "0" is amneziawg-go's zero UintRange: padding off, built-in timings.
	writeLineOrDefault(b, "content_padding_addition", opts.ContentPaddingAddition, "0")
	writeLineOrDefault(b, "rekey_after_time", opts.RekeyAfterTime, "0")
	writeLineOrDefault(b, "rekey_timeout", opts.RekeyTimeout, "0")
	writeLineOrDefault(b, "reject_after_time", opts.RejectAfterTime, "0")
	writeLineOrDefault(b, "keepalive_timeout", opts.KeepaliveTimeout, "0")
	writeLineOrDefault(b, "max_handshake_attempts", opts.MaxHandshakeAttempts, "0")
	fmt.Fprintf(b, "random_trailers=%t\n", opts.RandomTrailers)
	fmt.Fprintf(b, "disable_cookies=%t\n", opts.DisableCookies)
	return nil
}

// signaturePacketsFingerprint is I1-I5 as a live Device holds them. A
// running Device never gets new values: amneziawg-go keeps them in a plain
// array (Device.ipackets) that IpcSet assigns on every iN= line and that
// every handshake initiation reads with no lock, from the TUN reader and the
// timers, so an in-place IpcSet carrying those lines is a data race with
// live traffic (-race caught it in CI). The managers fold this into their
// rebuild fingerprint and send in-place configs through
// withoutSignaturePackets instead.
func signaturePacketsFingerprint(o amneziawg.Obfuscation31) string {
	return strings.Join([]string{o.I1, o.I2, o.I3, o.I4, o.I5}, "\n")
}

// withoutSignaturePackets drops the device-section iN= lines from a rendered
// UAPI config, for an in-place IpcSet on a running Device. An omitted line
// keeps the device's current value, which equals the new one because a
// changed signaturePacketsFingerprint rebuilds the Device instead.
func withoutSignaturePackets(conf string) string {
	var b strings.Builder
	b.Grow(len(conf))
	peers := false
	for _, line := range strings.SplitAfter(conf, "\n") {
		if strings.HasPrefix(line, "public_key=") {
			peers = true
		}
		if !peers && len(line) >= 3 && line[0] == 'i' && line[1] >= '1' && line[1] <= '5' && line[2] == '=' {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// writeLineOrDefault writes "name=v", or "name=def" when v is blank.
func writeLineOrDefault(b *strings.Builder, name, v, def string) {
	if v == "" {
		v = def
	}
	fmt.Fprintf(b, "%s=%s\n", name, v)
}
