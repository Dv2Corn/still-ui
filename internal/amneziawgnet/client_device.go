package amneziawgnet

import (
	"fmt"
	"strings"

	"github.com/amnezia-vpn/amneziawg-go/v3/device"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
)

// buildClientUAPIConfig renders a client-mode UAPI set string: the device
// lines of buildUAPIConfig plus per-peer endpoint/keepalive for dialing.
func buildClientUAPIConfig(inst amneziawg.OutboundInstance, opts DeviceOptions) (string, error) {
	var b strings.Builder

	privHex, err := wireguard.KeyToHex(inst.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("invalid private key: %w", err)
	}
	fmt.Fprintf(&b, "private_key=%s\n", privHex)
	if inst.ListenPort > 0 {
		fmt.Fprintf(&b, "listen_port=%d\n", inst.ListenPort)
	}
	b.WriteString("replace_peers=true\n")

	// Rows saved before these bounds existed would allocate without limit per handshake.
	if err := amneziawg.ValidateHandshakePreamble(inst.Obfuscation); err != nil {
		return "", err
	}
	if err := writeDeviceObfuscationLines(&b, inst.Obfuscation, opts); err != nil {
		return "", err
	}

	for _, p := range inst.Peers {
		pubHex, err := wireguard.KeyToHex(p.PublicKey)
		if err != nil {
			return "", fmt.Errorf("peer %q: invalid public key: %w", p.Endpoint, err)
		}
		fmt.Fprintf(&b, "public_key=%s\n", pubHex)
		if p.PresharedKey != "" {
			pskHex, err := wireguard.KeyToHex(p.PresharedKey)
			if err != nil {
				return "", fmt.Errorf("peer %q: invalid preshared key: %w", p.Endpoint, err)
			}
			fmt.Fprintf(&b, "preshared_key=%s\n", pskHex)
		}
		fmt.Fprintf(&b, "endpoint=%s\n", p.Endpoint)
		if p.KeepAlive > 0 {
			fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", p.KeepAlive)
		}
		for _, allowedIP := range p.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", allowedIP)
		}
	}

	return b.String(), nil
}

// newUnconfiguredClientDevice builds the tun/netstack/device trio for a
// client-mode instance; same construction rules as newUnconfiguredDevice.
func newUnconfiguredClientDevice(inst amneziawg.OutboundInstance, opts DeviceOptions) (*Device, error) {
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
		logger = device.NewLogger(device.LogLevelSilent, fmt.Sprintf("(awg-out %s) ", inst.Tag))
	}
	bind := newResolvingBind("")
	dev := device.NewDevice(tun, bind, logger)

	return &Device{Device: dev, Stack: gstack, localAddrs: addrs}, nil
}

// ConfigureClient applies inst/opts via UAPI and brings the interface up;
// same single-call contract as Configure.
func (d *Device) ConfigureClient(inst amneziawg.OutboundInstance, opts DeviceOptions) error {
	conf, err := buildClientUAPIConfig(inst, opts)
	if err != nil {
		d.Close()
		return fmt.Errorf("amneziawgnet: %w", err)
	}
	if err := d.IpcSet(conf); err != nil {
		d.Close()
		return fmt.Errorf("amneziawgnet: IpcSet for outbound %q: %w", inst.Tag, err)
	}
	if err := d.Up(); err != nil {
		d.Close()
		return fmt.Errorf("amneziawgnet: bring up outbound %q: %w", inst.Tag, err)
	}
	return nil
}
