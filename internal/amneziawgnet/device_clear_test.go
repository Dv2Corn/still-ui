package amneziawgnet

import (
	"sort"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
)

// deviceDump returns d's UAPI "get" lines minus listen_port (each test device
// binds its own free port), sorted so peer-map order cannot matter.
func deviceDump(t *testing.T, d *Device) string {
	t.Helper()
	raw, err := d.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		if l == "" || strings.HasPrefix(l, "listen_port=") {
			continue
		}
		lines = append(lines, l)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func clearTestInstance(t *testing.T) amneziawg.Instance {
	t.Helper()
	priv, pub, err := wireguard.GenerateWireguardKeypair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	_, clientPub, err := wireguard.GenerateWireguardKeypair()
	if err != nil {
		t.Fatalf("generate client keypair: %v", err)
	}
	return amneziawg.Instance{
		Id: 91, InterfaceName: "awgclear", PrivateKey: priv, PublicKey: pub,
		Address: []string{"10.209.0.1/24"}, MTU: 1420,
		Obfuscation: amneziawg.Obfuscation31{Jc: 4, Jmin: 40, Jmax: 70, S1: 20, S2: 30, S3: 20, S4: 20},
		Peers:       []amneziawg.Peer{{Email: "a@x", PublicKey: clientPub, AllowedIPs: []string{"10.209.0.2/32"}}},
	}
}

// Clearing H1-H4, I1-I5, content padding or the timings must reach a running
// device the same way a fresh device would get them: an omitted UAPI line means
// "keep the old value" to amneziawg-go, so the old H values stayed live while
// clients re-downloading their config got the defaults and could not handshake.
func TestInPlaceReconfigureAfterClearingMatchesFreshDevice(t *testing.T) {
	set := clearTestInstance(t)
	set.ListenPort = freeUDPPort(t)
	set.Obfuscation.H1, set.Obfuscation.H2, set.Obfuscation.H3, set.Obfuscation.H4 = "100", "200", "300", "400"
	set.Obfuscation.I1 = "<r 32>"
	set.Obfuscation.I3 = "<b 0xabcd>"
	setOpts := DeviceOptions{
		ContentPaddingAddition: "8-16",
		RekeyAfterTime:         "100-110", RekeyTimeout: "4-6", RejectAfterTime: "150-160",
		KeepaliveTimeout: "9-11", MaxHandshakeAttempts: "15-20",
	}

	cleared := set
	cleared.Obfuscation.H1, cleared.Obfuscation.H2, cleared.Obfuscation.H3, cleared.Obfuscation.H4 = "", "", "", ""
	cleared.Obfuscation.I1, cleared.Obfuscation.I3 = "", ""

	live, err := NewDevice(set, setOpts)
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	defer live.Close()
	conf, err := buildUAPIConfig(cleared, DeviceOptions{})
	if err != nil {
		t.Fatalf("buildUAPIConfig: %v", err)
	}
	if err := live.IpcSet(conf); err != nil {
		t.Fatalf("in-place IpcSet after clearing: %v", err)
	}

	freshInst := cleared
	freshInst.ListenPort = freeUDPPort(t)
	fresh, err := NewDevice(freshInst, DeviceOptions{})
	if err != nil {
		t.Fatalf("NewDevice fresh: %v", err)
	}
	defer fresh.Close()

	if got, want := deviceDump(t, live), deviceDump(t, fresh); got != want {
		t.Fatalf("in-place clear left stale device state\nreconfigured:\n%s\nfresh:\n%s", got, want)
	}
}

// Blanking H1 while moving H2 onto the old H1 value is a valid edit (blank H1
// means type 1), but the stale H1 made IpcSet fail "headers must not overlap"
// on every reconcile, after replace_peers had already dropped every peer.
func TestInPlaceReconfigurePartialHClearDoesNotOverlapStaleValue(t *testing.T) {
	set := clearTestInstance(t)
	set.ListenPort = freeUDPPort(t)
	set.Obfuscation.H1, set.Obfuscation.H2, set.Obfuscation.H3, set.Obfuscation.H4 = "100", "200", "300", "400"

	next := set
	next.Obfuscation.H1, next.Obfuscation.H2 = "", "100"
	if err := amneziawg.ValidateObfuscation(next.Obfuscation); err != nil {
		t.Fatalf("the edit itself must be accepted at save time: %v", err)
	}

	live, err := NewDevice(set, DeviceOptions{})
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	defer live.Close()
	conf, err := buildUAPIConfig(next, DeviceOptions{})
	if err != nil {
		t.Fatalf("buildUAPIConfig: %v", err)
	}
	if err := live.IpcSet(conf); err != nil {
		t.Fatalf("in-place IpcSet failed: %v", err)
	}
	if dump := "\n" + deviceDump(t, live) + "\n"; !strings.Contains(dump, "\nh1=1\n") || !strings.Contains(dump, "\nh2=100\n") {
		t.Fatalf("device did not take h1=1/h2=100:\n%s", dump)
	}
}

// One malformed client key used to fail the whole UAPI config, so the inbound
// kept its old peer list forever: new clients never connected, removed ones stayed.
func TestBuildUAPIConfigSkipsPeerWithMalformedKey(t *testing.T) {
	inst := clearTestInstance(t)
	good := inst.Peers[0]
	inst.Peers = append(inst.Peers,
		amneziawg.Peer{Email: "badpub", PublicKey: "not-a-key", AllowedIPs: []string{"10.209.0.3/32"}},
		amneziawg.Peer{Email: "badpsk", PublicKey: good.PublicKey[:43] + "=", PresharedKey: "short", AllowedIPs: []string{"10.209.0.4/32"}},
	)
	conf, err := buildUAPIConfig(inst, DeviceOptions{})
	if err != nil {
		t.Fatalf("one bad peer failed the whole inbound: %v", err)
	}
	if n := strings.Count(conf, "public_key="); n != 1 {
		t.Fatalf("want only the valid peer, got %d peers:\n%s", n, conf)
	}
	if strings.Contains(conf, "10.209.0.3/32") || strings.Contains(conf, "10.209.0.4/32") {
		t.Fatalf("malformed peers reached the device:\n%s", conf)
	}
}
