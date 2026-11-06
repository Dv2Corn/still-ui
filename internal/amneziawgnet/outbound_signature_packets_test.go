package amneziawgnet

import (
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
)

// signatureTestOutbound is a client-mode outbound with one peer at a local
// endpoint, so traffic into the tunnel makes it send handshake initiations.
func signatureTestOutbound(t *testing.T, tag string) (OutboundDesired, [2]string) {
	t.Helper()
	d := newTestOutboundDesired(t, tag)
	d.Instance.Address = []string{"10.212.0.1/24"}
	d.Instance.Obfuscation = amneziawg.Obfuscation31{Jc: 2, Jmin: 40, Jmax: 70, S1: 20, S2: 30, S3: 20, S4: 20, I1: "<r 16>"}
	var peerPubs [2]string
	for i := range peerPubs {
		var err error
		if _, peerPubs[i], err = wireguard.GenerateWireguardKeypair(); err != nil {
			t.Fatalf("generate peer keypair: %v", err)
		}
	}
	d.Instance.Peers = []amneziawg.OutboundPeer{{
		PublicKey: peerPubs[0], AllowedIPs: []string{"10.212.0.0/24"},
		Endpoint: "127.0.0.1:" + strconv.Itoa(freeUDPPort(t)),
	}}
	return d, peerPubs
}

// #57 review: without the I1-I5 term in the outbound fingerprint an edit to
// them took the in-place path, which drops iN=, and never reached the Device.
func TestOutboundSignaturePacketEditRebuildsAndReachesTheDevice(t *testing.T) {
	for _, tc := range []struct {
		name     string
		from, to string
	}{
		{"set", "", "<r 24>"},
		{"change", "<r 24>", "<b 0xabcd>"},
		{"clear", "<b 0xabcd>", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &OutboundManager{iface: map[string]*managedOutbound{}}
			defer m.Reconcile(nil)
			d, peerPubs := signatureTestOutbound(t, "sig-"+tc.name)
			d.Instance.Obfuscation.I2 = tc.from
			m.Reconcile([]OutboundDesired{d})
			cur := m.iface[d.Instance.Tag]
			if cur == nil {
				t.Fatal("outbound did not come up")
			}

			next := d
			next.Instance.Obfuscation.I2 = tc.to
			m.Reconcile([]OutboundDesired{next})
			rebuilt := m.iface[d.Instance.Tag]
			if rebuilt == nil || rebuilt.dev == cur.dev {
				t.Fatal("I2 edit was applied in place; it must rebuild the Device")
			}
			dump := "\n" + deviceDump(t, rebuilt.dev) + "\n"
			if tc.to == "" && strings.Contains(dump, "\ni2=") {
				t.Fatalf("cleared I2 still on the device:\n%s", dump)
			}
			if tc.to != "" && !strings.Contains(dump, "\ni2="+tc.to+"\n") {
				t.Fatalf("device does not carry i2=%s:\n%s", tc.to, dump)
			}

			again := next
			again.Instance.Peers = []amneziawg.OutboundPeer{next.Instance.Peers[0]}
			again.Instance.Peers[0].PublicKey = peerPubs[1]
			m.Reconcile([]OutboundDesired{again})
			if got := m.iface[d.Instance.Tag]; got == nil || got.dev != rebuilt.dev {
				t.Fatal("peer-only edit rebuilt the Device")
			}
			if got := "\n" + deviceDump(t, rebuilt.dev) + "\n"; strings.Contains(got, "\ni2=") != (tc.to != "") {
				t.Fatalf("peer-only edit changed I2:\n%s", got)
			}
		})
	}
}

// A client-mode Device initiates every handshake, so an in-place IpcSet that
// re-sent iN= lines would race that path; -race fails this without the strip.
func TestOutboundInPlaceReconfigureWhileHandshakingDoesNotRewriteSignaturePackets(t *testing.T) {
	m := &OutboundManager{iface: map[string]*managedOutbound{}}
	defer m.Reconcile(nil)
	d, peerPubs := signatureTestOutbound(t, "sig-race")
	m.Reconcile([]OutboundDesired{d})
	cur := m.iface[d.Instance.Tag]
	if cur == nil {
		t.Fatal("outbound did not come up")
	}

	target := tunnelFullAddress(netip.MustParseAddr("10.212.0.9"), 9)
	conn, err := gonet.DialUDP(cur.dev.Stack, nil, &target, tunnelNetwork(netip.MustParseAddr("10.212.0.9")))
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		payload := make([]byte, 64)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = conn.Write(payload)
		}
	}()

	iterations := 300
	if testing.Short() {
		iterations = 50
	}
	for i := 0; i < iterations; i++ {
		next := d
		next.Instance.Peers = []amneziawg.OutboundPeer{d.Instance.Peers[0]}
		next.Instance.Peers[0].PublicKey = peerPubs[(i+1)%2]
		m.Reconcile([]OutboundDesired{next})
		if got := m.iface[d.Instance.Tag]; got == nil || got.dev != cur.dev {
			close(stop)
			wg.Wait()
			t.Fatalf("peer-only change #%d rebuilt the outbound", i)
		}
	}
	close(stop)
	wg.Wait()
}
