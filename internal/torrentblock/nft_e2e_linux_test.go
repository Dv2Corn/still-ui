//go:build linux

package torrentblock

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The e2e test rebuilds a small network inside the current (throwaway) host:
// netns "sutb-out" plays the internet, netns "sutb-cli" an AmneziaWG kernel client.
const (
	e2eOutNS   = "sutb-out"
	e2eCliNS   = "sutb-cli"
	e2ePeer    = "192.0.2.10"
	e2eCliPeer = "192.0.2.11" // fresh peer: e2ePeer is already a known swarm member by then
	e2eUDPPeer = "198.51.100.10"
	e2eCliAddr = "10.99.0.2"
)

func e2eSh(t *testing.T, cmd string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
	return string(out)
}

func e2eTopology(t *testing.T) {
	t.Helper()
	teardown := func() {
		_ = exec.Command("sh", "-c", "pkill -f 'socat .*sutb-r[x]' ; ip netns del "+e2eOutNS+"; ip netns del "+e2eCliNS+"; ip link del wan9; ip link del xui-awg9").Run()
	}
	teardown()
	t.Cleanup(teardown)
	e2eSh(t, `set -e
ip netns add `+e2eOutNS+`
ip link add wan9 type veth peer name eth0 netns `+e2eOutNS+`
ip addr add 192.0.2.1/24 dev wan9 && ip link set wan9 up
ip -n `+e2eOutNS+` addr add `+e2ePeer+`/24 dev eth0
ip -n `+e2eOutNS+` addr add `+e2eCliPeer+`/24 dev eth0
ip -n `+e2eOutNS+` addr add `+e2eUDPPeer+`/24 dev eth0
ip -n `+e2eOutNS+` link set eth0 up && ip -n `+e2eOutNS+` link set lo up
ip -n `+e2eOutNS+` route add default via 192.0.2.1
ip route replace 198.51.100.0/24 via `+e2ePeer+`
ip netns add `+e2eCliNS+`
ip link add xui-awg9 type veth peer name eth0 netns `+e2eCliNS+`
ip addr add 10.99.0.1/24 dev xui-awg9 && ip link set xui-awg9 up
ip -n `+e2eCliNS+` addr add `+e2eCliAddr+`/24 dev eth0
ip -n `+e2eCliNS+` link set eth0 up && ip -n `+e2eCliNS+` link set lo up
ip -n `+e2eCliNS+` route add default via 10.99.0.1
sysctl -qw net.ipv4.ip_forward=1
rm -f /tmp/sutb-rx-*
for p in 6881 6882 443 8080; do
  ip netns exec `+e2eOutNS+` socat -u TCP-LISTEN:$p,reuseaddr,fork OPEN:/tmp/sutb-rx-tcp$p,creat,append </dev/null >/dev/null 2>&1 &
done
for p in 6881 6882 6883 443; do
  ip netns exec `+e2eOutNS+` socat -u UDP-RECV:$p OPEN:/tmp/sutb-rx-udp$p,creat,append </dev/null >/dev/null 2>&1 &
done
sleep 0.5`)
}

func rxBytes(name string) int {
	b, _ := os.ReadFile("/tmp/sutb-rx-" + name)
	return len(b)
}

func btHandshakeMsg() []byte {
	msg := append([]byte("\x13BitTorrent protocol"), make([]byte, 8)...)
	msg = append(msg, bytes.Repeat([]byte{0xab}, 20)...)
	return append(msg, []byte("-TR4060-000000000000")...)
}

func sendTCP(addr string, payload []byte) error {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write(payload)
	time.Sleep(400 * time.Millisecond)
	return err
}

func sendUDP(t *testing.T, addr string, payload []byte) {
	t.Helper()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer c.Close()
	_, _ = c.Write(payload)
	time.Sleep(200 * time.Millisecond)
}

func utpSyn() []byte {
	h := make([]byte, 20)
	h[0] = 0x41
	binary.BigEndian.PutUint16(h[2:], 0x1234)
	binary.BigEndian.PutUint32(h[4:], 123456)
	binary.BigEndian.PutUint32(h[12:], 0x10000)
	binary.BigEndian.PutUint16(h[16:], 1)
	return h
}

func trackerConnectMsg() []byte {
	return append(append([]byte{}, trackerConnect...), 0xde, 0xad, 0xbe, 0xef)
}

func cliTCP(addr string, payload []byte) error {
	script := "import socket,sys,time\ns=socket.create_connection((sys.argv[1],int(sys.argv[2])),2)\ns.sendall(bytes.fromhex(sys.argv[3]))\ntime.sleep(0.4)\n"
	host, port, _ := net.SplitHostPort(addr)
	return exec.Command("ip", "netns", "exec", e2eCliNS, "python3", "-c", script, host, port, hex.EncodeToString(payload)).Run()
}

func ruleCount(t *testing.T) int {
	t.Helper()
	return strings.Count(e2eSh(t, "nft list table inet "+TableName), "\n")
}

// Applies the real table to the real kernel and drives real BitTorrent packets at it.
func TestNftE2E(t *testing.T) {
	if os.Getenv("XUI_NFT_E2E") != "1" {
		t.Skip("set XUI_NFT_E2E=1 on a disposable Linux host (root, nft, socat, python3)")
	}
	m := Default()
	_ = m.Remove()
	e2eTopology(t)
	t.Cleanup(func() { _ = m.Remove() })

	httpsBefore := exec.Command("curl", "-sfo", "/dev/null", "-m", "8", "https://deb.debian.org/").Run() == nil

	if err := sendTCP(e2ePeer+":6881", btHandshakeMsg()); err != nil || rxBytes("tcp6881") == 0 {
		t.Fatalf("baseline: handshake did not reach the peer without the table (err %v)", err)
	}
	base := rxBytes("tcp6881")

	if err := m.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	rules := ruleCount(t)
	if err := m.Apply(); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if again := ruleCount(t); again != rules {
		t.Fatalf("re-applying changed the table from %d to %d lines", rules, again)
	}

	t.Run("server egress", func(t *testing.T) {
		_ = sendTCP(e2ePeer+":6881", btHandshakeMsg())
		if got := rxBytes("tcp6881"); got != base {
			t.Fatalf("BT handshake reached the peer: %d bytes, want %d", got, base)
		}
		if err := sendTCP(e2ePeer+":6882", []byte("plain")); err == nil {
			t.Fatal("a known swarm peer stayed reachable on a non-web port")
		}
		if err := sendTCP(e2ePeer+":443", []byte("GET / HTTP/1.1\r\n\r\n")); err != nil || rxBytes("tcp443") == 0 {
			t.Fatalf("port 443 of a swarm peer was cut: %v", err)
		}
		sendUDP(t, e2eUDPPeer+":6881", append([]byte("d1:ad2:id20:"), bytes.Repeat([]byte("x"), 40)...))
		sendUDP(t, e2eUDPPeer+":6882", trackerConnectMsg())
		sendUDP(t, e2eUDPPeer+":6883", utpSyn())
		for _, f := range []string{"udp6881", "udp6882", "udp6883"} {
			if n := rxBytes(f); n != 0 {
				t.Fatalf("%s: %d bytes reached the peer", f, n)
			}
		}
		sendUDP(t, e2eUDPPeer+":443", []byte("ordinary quic-ish datagram"))
		if rxBytes("udp443") == 0 {
			t.Fatal("UDP 443 to the peer was cut")
		}
		c := m.Status().Counters
		if c.Handshakes == 0 || c.DHT == 0 || c.Trackers == 0 || c.UTP == 0 || c.PeerDrops == 0 {
			t.Fatalf("counters = %+v, want every signature and a peer drop counted", c)
		}
		if httpsBefore && exec.Command("curl", "-sfo", "/dev/null", "-m", "8", "https://deb.debian.org/").Run() != nil {
			t.Fatal("ordinary HTTPS stopped working with the table loaded")
		}
		t.Logf("counters %+v, real HTTPS checked: %v", c, httpsBefore)
	})

	t.Run("kernel AmneziaWG client", func(t *testing.T) {
		before := rxBytes("tcp6881")
		if err := cliTCP(e2eCliPeer+":6881", btHandshakeMsg()); err != nil {
			t.Fatalf("tunnel client could not open the connection: %v", err)
		}
		if rxBytes("tcp6881") != before {
			t.Fatal("forwarded BT handshake reached the peer")
		}
		if !strings.Contains(e2eSh(t, "nft list set inet "+TableName+" ban4"), e2eCliAddr) {
			t.Fatal("tunnel client that sent a handshake is not in ban4")
		}
		if err := cliTCP(e2eCliPeer+":8080", []byte("plain")); err == nil {
			t.Fatal("banned tunnel client still reaches a non-web port")
		}
		if err := cliTCP(e2eCliPeer+":443", []byte("GET / HTTP/1.1\r\n\r\n")); err != nil {
			t.Fatalf("banned tunnel client lost port 443: %v", err)
		}
		if c := m.Status().Counters; c.TunnelBans == 0 {
			t.Fatalf("counters = %+v, want a tunnel ban hit", c)
		}
	})

	t.Run("ensure keeps live sets", func(t *testing.T) {
		if err := m.Ensure(); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if !strings.Contains(e2eSh(t, "nft list set inet "+TableName+" peer4"), e2ePeer) {
			t.Fatal("Ensure on a loaded table dropped the learned peers")
		}
		e2eSh(t, "nft delete table inet "+TableName)
		if err := m.Ensure(); err != nil || !m.Status().Active {
			t.Fatalf("Ensure did not restore a deleted table: %v", err)
		}
	})

	if err := m.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if strings.Contains(e2eSh(t, "nft list tables"), TableName) || m.Status().Active {
		t.Fatal("table still present after Remove")
	}
	if err := m.Remove(); err != nil {
		t.Fatalf("Remove on a clean host: %v", err)
	}
}
