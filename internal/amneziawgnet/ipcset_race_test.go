package amneziawgnet

import (
	"net/netip"
	"strings"
	"sync"
	"testing"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
)

// A peer-only edit reconfigures the live device in place while its TUN reader
// is sending handshake initiations. amneziawg-go keeps I1-I5 in a plain array
// that every initiation reads without a lock, and IpcSet assigns it on every
// iN= line, so the in-place config must not carry those lines (-race caught
// this in TestEnsureDropsFlowsOfReassignedTunnelAddress, intermittently).
func TestInPlaceReconfigureWhileHandshakingDoesNotRewriteSignaturePackets(t *testing.T) {
	priv, pub, err := wireguard.GenerateWireguardKeypair()
	if err != nil {
		t.Fatalf("generate server keypair: %v", err)
	}
	var peerPubs [2]string
	for i := range peerPubs {
		if _, peerPubs[i], err = wireguard.GenerateWireguardKeypair(); err != nil {
			t.Fatalf("generate peer keypair: %v", err)
		}
	}
	m := &Manager{ifaces: map[int]*managed{}}
	defer m.StopAll()
	inst := amneziawg.Instance{
		Id: 92, InterfaceName: "awgrace92", PrivateKey: priv, PublicKey: pub,
		Address: []string{"10.211.0.1/24"}, MTU: 1420,
		Obfuscation: amneziawg.Obfuscation31{
			Jc: 2, Jmin: 40, Jmax: 70, S1: 20, S2: 30, S3: 20, S4: 20,
			I1: "<r 16>",
		},
		Peers: []amneziawg.Peer{{Email: "a@test", PublicKey: peerPubs[0], AllowedIPs: []string{"10.211.0.2/32"}}},
	}
	if err := m.Ensure(Desired{Instance: inst}); err != nil {
		t.Fatalf("Ensure (create): %v", err)
	}
	cur := m.ifaces[inst.Id]

	peerAddr := tunnelFullAddress(netip.MustParseAddr("10.211.0.2"), 9)
	conn, err := gonet.DialUDP(cur.dev.Stack, nil, &peerAddr, tunnelNetwork(netip.MustParseAddr("10.211.0.2")))
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()

	// Each in-place reconfigure replaces every peer, so the next packet
	// toward the address is a fresh peer's first handshake initiation.
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
		next := inst
		next.Peers = []amneziawg.Peer{{
			Email: "a@test", PublicKey: peerPubs[(i+1)%2], AllowedIPs: []string{"10.211.0.2/32"},
		}}
		if err := m.Ensure(Desired{Instance: next}); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("Ensure (in place) #%d: %v", i, err)
		}
		if m.ifaces[inst.Id] != cur {
			close(stop)
			wg.Wait()
			t.Fatalf("peer-only change #%d rebuilt the interface", i)
		}
	}
	close(stop)
	wg.Wait()
}

func TestWithoutSignaturePacketsKeepsEverythingElse(t *testing.T) {
	inst := clearTestInstance(t)
	inst.Obfuscation.I1, inst.Obfuscation.I4 = "<r 32>", "<b 0xabcd>"
	conf, err := buildUAPIConfig(inst, DeviceOptions{})
	if err != nil {
		t.Fatalf("buildUAPIConfig: %v", err)
	}
	got := withoutSignaturePackets(conf)
	var want strings.Builder
	for _, line := range strings.SplitAfter(conf, "\n") {
		switch {
		case strings.HasPrefix(line, "i1="), strings.HasPrefix(line, "i2="), strings.HasPrefix(line, "i3="),
			strings.HasPrefix(line, "i4="), strings.HasPrefix(line, "i5="):
		default:
			want.WriteString(line)
		}
	}
	if got != want.String() {
		t.Fatalf("withoutSignaturePackets:\n%s\nwant:\n%s", got, want.String())
	}
	if strings.Count(conf, "\n") != strings.Count(got, "\n")+5 {
		t.Fatalf("want exactly the five iN= lines dropped:\n%s", got)
	}
	if !strings.Contains(got, "\npublic_key=") || !strings.Contains(got, "\nallowed_ip=10.209.0.2/32\n") {
		t.Fatalf("peer section lost:\n%s", got)
	}
}

// Because I1-I5 never reach a running Device in place, an edit to them has to
// rebuild it -- otherwise the edit would silently never take effect.
func TestSignaturePacketEditRebuildsAndReachesTheDevice(t *testing.T) {
	for _, tc := range []struct {
		name     string
		from, to string
	}{
		{"set", "", "<r 24>"},
		{"change", "<r 24>", "<b 0xabcd>"},
		{"clear", "<b 0xabcd>", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{ifaces: map[int]*managed{}}
			defer m.StopAll()
			inst := clearTestInstance(t)
			inst.Obfuscation.I2 = tc.from
			if err := m.Ensure(Desired{Instance: inst}); err != nil {
				t.Fatalf("Ensure (create): %v", err)
			}
			cur := m.ifaces[inst.Id]

			next := inst
			next.Obfuscation.I2 = tc.to
			if err := m.Ensure(Desired{Instance: next}); err != nil {
				t.Fatalf("Ensure (I2 edit): %v", err)
			}
			rebuilt := m.ifaces[next.Id]
			if rebuilt == cur {
				t.Fatal("I2 edit was applied in place; it must rebuild the Device")
			}
			dump := "\n" + deviceDump(t, rebuilt.dev) + "\n"
			if tc.to == "" && strings.Contains(dump, "\ni2=") {
				t.Fatalf("cleared I2 still on the device:\n%s", dump)
			}
			if tc.to != "" && !strings.Contains(dump, "\ni2="+tc.to+"\n") {
				t.Fatalf("device does not carry i2=%s:\n%s", tc.to, dump)
			}

			// A peer-only edit afterwards stays in place and keeps I2.
			again := next
			again.Peers = nil
			if err := m.Ensure(Desired{Instance: again}); err != nil {
				t.Fatalf("Ensure (peer edit): %v", err)
			}
			if m.ifaces[again.Id] != rebuilt {
				t.Fatal("peer-only edit rebuilt the Device")
			}
			if got := "\n" + deviceDump(t, rebuilt.dev) + "\n"; strings.Contains(got, "\ni2=") != (tc.to != "") {
				t.Fatalf("peer-only edit changed I2:\n%s", got)
			}
		})
	}
}
