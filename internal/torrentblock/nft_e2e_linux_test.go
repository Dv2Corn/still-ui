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

// The e2e test rebuilds a small network inside the current (throwaway) host: netns
// "sutb-out" plays the internet, "sutb-cli"/"sutb-cli2" are AmneziaWG kernel clients.
const (
	e2eOutNS   = "sutb-out"
	e2eCliNS   = "sutb-cli"
	e2eCli2NS  = "sutb-cli2"
	e2ePeer    = "192.0.2.10"
	e2ePeer2   = "192.0.2.11"
	e2eUDPPeer = "198.51.100.10"
	e2eCliAddr = "10.99.0.2"
	e2eCli2    = "10.99.1.2"
)

// e2ePeerScript runs inside sutb-out: sinks record what arrives, responders answer
// first with a BitTorrent reply (handshake, DHT response, uTP ST_STATE).
const e2ePeerScript = `
import socket, threading
HS = b"\x13BitTorrent protocol" + bytes(8) + b"\xab" * 20 + b"-XX0000-000000000000"
def rec(name, data):
    with open("/tmp/sutb-rx-" + name, "ab") as f:
        f.write(data)
def tcp(port, reply):
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("0.0.0.0", port)); s.listen(64)
    while True:
        c, _ = s.accept()
        def serve(c=c):
            try:
                if reply:
                    c.sendall(HS)
                while True:
                    d = c.recv(4096)
                    if not d:
                        break
                    rec("tcp%d" % port, d)
            except OSError:
                pass
            c.close()
        threading.Thread(target=serve, daemon=True).start()
def udp(port, reply, addr="0.0.0.0"):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.bind((addr, port))
    while True:
        d, a = s.recvfrom(4096)
        rec("udp%d" % port, d)
        if reply:
            s.sendto(reply, a)
UTP_STATE = bytes([0x21, 0]) + bytes(18)
DHT_R = b"d1:rd2:id20:" + b"x" * 20 + b"e1:t2:aa1:y1:re"
for p in (443, 6881, 6882, 8080):
    threading.Thread(target=tcp, args=(p, False), daemon=True).start()
for p in (80, 6890, 9443):
    threading.Thread(target=tcp, args=(p, True), daemon=True).start()
for p in (443, 6881, 6882, 6883):
    threading.Thread(target=udp, args=(p, None), daemon=True).start()
# Bound to the address so the answer leaves from it and conntrack sees a reply.
threading.Thread(target=udp, args=(6895, DHT_R, "198.51.100.10"), daemon=True).start()
threading.Thread(target=udp, args=(6896, UTP_STATE, "198.51.100.10"), daemon=True).start()
threading.Event().wait()
`

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
		_ = exec.Command("sh", "-c", "pkill -f 'sutb-pee[r]' ; for n in "+e2eOutNS+" "+e2eCliNS+" "+e2eCli2NS+"; do ip netns del $n; done; ip link del wan9; ip link del xui-awg9; ip link del xui-awg8").Run()
	}
	teardown()
	t.Cleanup(teardown)
	if err := os.WriteFile("/tmp/sutb-peer.py", []byte(e2ePeerScript), 0o644); err != nil {
		t.Fatal(err)
	}
	e2eSh(t, `set -e
ip netns add `+e2eOutNS+`
ip link add wan9 type veth peer name eth0 netns `+e2eOutNS+`
ip addr add 192.0.2.1/24 dev wan9 && ip link set wan9 up
for a in `+e2ePeer+` `+e2ePeer2+` `+e2eUDPPeer+`; do ip -n `+e2eOutNS+` addr add $a/24 dev eth0; done
ip -n `+e2eOutNS+` link set eth0 up && ip -n `+e2eOutNS+` link set lo up
ip -n `+e2eOutNS+` route add default via 192.0.2.1
ip route replace 198.51.100.0/24 via `+e2ePeer+`
mk() { ip netns add $1; ip link add $2 type veth peer name eth0 netns $1
  ip addr add $3.1/24 dev $2 && ip link set $2 up
  ip -n $1 addr add $3.2/24 dev eth0; ip -n $1 link set eth0 up; ip -n $1 link set lo up
  ip -n $1 route add default via $3.1; }
mk `+e2eCliNS+` xui-awg9 10.99.0
mk `+e2eCli2NS+` xui-awg8 10.99.1
sysctl -qw net.ipv4.ip_forward=1
rm -f /tmp/sutb-rx-*
ip netns exec `+e2eOutNS+` python3 /tmp/sutb-peer.py </dev/null >/dev/null 2>&1 &
sleep 1`)
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
	time.Sleep(300 * time.Millisecond)
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

func nsTCP(ns, addr string, payload []byte) error {
	script := "import socket,sys,time\ns=socket.create_connection((sys.argv[1],int(sys.argv[2])),2)\ns.sendall(bytes.fromhex(sys.argv[3]))\ntime.sleep(0.4)\n"
	host, port, _ := net.SplitHostPort(addr)
	return exec.Command("ip", "netns", "exec", ns, "python3", "-c", script, host, port, hex.EncodeToString(payload)).Run()
}

func setHas(t *testing.T, set, elem string) bool {
	t.Helper()
	return strings.Contains(e2eSh(t, "nft list set inet "+TableName+" "+set), elem)
}

func ruleCount(t *testing.T) int {
	t.Helper()
	return strings.Count(e2eSh(t, "nft list table inet "+TableName), "\n")
}

// Applies the real table to the real kernel and drives real BitTorrent packets at it.
func TestNftE2E(t *testing.T) {
	if os.Getenv("XUI_NFT_E2E") != "1" {
		t.Skip("set XUI_NFT_E2E=1 on a disposable Linux host (root, nft, python3)")
	}
	m := Default()
	_ = m.Remove()
	e2eTopology(t)
	t.Cleanup(func() { _ = m.Remove() })

	httpsBefore := exec.Command("curl", "-sfo", "/dev/null", "-m", "8", "https://deb.debian.org/").Run() == nil
	if err := sendTCP(e2ePeer+":6881", btHandshakeMsg()); err != nil || rxBytes("tcp6881") == 0 {
		t.Fatalf("baseline: handshake did not reach the peer without the table (err %v)", err)
	}

	protected := []uint16{9443}
	if err := m.Apply(protected); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	rules := ruleCount(t)
	if err := m.Apply(protected); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if again := ruleCount(t); again != rules {
		t.Fatalf("re-applying changed the table from %d to %d lines", rules, again)
	}

	t.Run("outbound signatures are dropped but teach nothing", func(t *testing.T) {
		before := rxBytes("tcp443")
		_ = sendTCP(e2ePeer+":443", btHandshakeMsg())
		if rxBytes("tcp443") != before {
			t.Fatal("a BT handshake to port 443 reached the peer")
		}
		if err := sendTCP(e2ePeer+":443", []byte("GET / HTTP/1.1\r\n\r\n")); err != nil || rxBytes("tcp443") == before {
			t.Fatalf("a fake handshake to %s:443 got ordinary traffic there cut: %v", e2ePeer, err)
		}
		base := rxBytes("tcp6881")
		_ = sendTCP(e2ePeer+":6881", btHandshakeMsg())
		if rxBytes("tcp6881") != base {
			t.Fatal("BT handshake reached the peer")
		}
		for _, port := range []string{"6881", "6882"} {
			if err := sendTCP(e2ePeer+":"+port, []byte("plain")); err != nil {
				t.Fatalf("an outbound handshake alone cut %s:%s: %v", e2ePeer, port, err)
			}
		}
		sendUDP(t, e2eUDPPeer+":6881", append([]byte("d1:ad2:id20:"), bytes.Repeat([]byte("x"), 40)...))
		sendUDP(t, e2eUDPPeer+":6882", trackerConnectMsg())
		sendUDP(t, e2eUDPPeer+":6883", utpSyn())
		for _, f := range []string{"udp6881", "udp6882", "udp6883"} {
			if n := rxBytes(f); n != 0 {
				t.Fatalf("%s: %d bytes reached the peer", f, n)
			}
		}
		if out := e2eSh(t, "nft list set inet "+TableName+" peer4"); strings.Contains(out, "elements") {
			t.Fatalf("outbound packets taught peers:\n%s", out)
		}
	})

	t.Run("a peer is learned from its own BitTorrent answer", func(t *testing.T) {
		if err := sendTCP(e2ePeer+":6890", []byte("hello")); err != nil {
			t.Fatalf("first connection to the answering peer: %v", err)
		}
		if !setHas(t, "peer4", e2ePeer+" . 6890") {
			t.Fatal("a peer that answered with a BT handshake was not learned")
		}
		if err := sendTCP(e2ePeer+":6890", []byte("hello")); err == nil {
			t.Fatal("the learned peer endpoint is still reachable")
		}
		if err := sendTCP(e2ePeer+":6882", []byte("plain")); err != nil {
			t.Fatalf("learning one endpoint cut another port of the same address: %v", err)
		}
		before := rxBytes("udp6895")
		sendUDP(t, e2eUDPPeer+":6895", []byte("ping"))
		sendUDP(t, e2eUDPPeer+":6895", []byte("ping"))
		if !setHas(t, "peer4", e2eUDPPeer+" . 6895") {
			t.Fatal("a DHT response did not teach its endpoint")
		}
		if got := rxBytes("udp6895"); got != before+4 {
			t.Fatalf("the learned DHT endpoint still got %d bytes, want %d", got, before+4)
		}
		sendUDP(t, e2eUDPPeer+":6896", []byte("ping"))
		if !setHas(t, "peer4", e2eUDPPeer+" . 6896") {
			t.Fatal("a uTP ST_STATE answer did not teach its endpoint")
		}
	})

	t.Run("service and protected ports are never learned", func(t *testing.T) {
		for _, port := range []string{"80", "9443"} {
			_ = sendTCP(e2ePeer2+":"+port, []byte("hello"))
			if setHas(t, "peer4", e2ePeer2+" . "+port) {
				t.Fatalf("a BT answer from port %s was learned", port)
			}
			if err := sendTCP(e2ePeer2+":"+port, []byte("hello")); err != nil {
				t.Fatalf("port %s cut after a BT answer: %v", port, err)
			}
		}
		c := m.Status().Counters
		if c.Handshakes == 0 || c.DHT == 0 || c.Trackers == 0 || c.UTP == 0 || c.Learned < 3 || c.PeerDrops == 0 {
			t.Fatalf("counters = %+v, want every signature, three learned peers and a peer drop", c)
		}
		if httpsBefore && exec.Command("curl", "-sfo", "/dev/null", "-m", "8", "https://deb.debian.org/").Run() != nil {
			t.Fatal("ordinary HTTPS stopped working with the table loaded")
		}
		t.Logf("counters %+v, real HTTPS checked: %v", c, httpsBefore)
	})

	t.Run("kernel AmneziaWG clients", func(t *testing.T) {
		before := rxBytes("tcp6881")
		if err := nsTCP(e2eCliNS, e2ePeer2+":6881", btHandshakeMsg()); err != nil {
			t.Fatalf("tunnel client could not open the connection: %v", err)
		}
		if rxBytes("tcp6881") != before {
			t.Fatal("forwarded BT handshake reached the peer")
		}
		if !setHas(t, "ban4", e2eCliAddr) || setHas(t, "ban4", e2eCli2) {
			t.Fatal("ban4 does not hold exactly the offending tunnel client")
		}
		if err := nsTCP(e2eCliNS, e2ePeer2+":8080", []byte("plain")); err == nil {
			t.Fatal("banned tunnel client still reaches a non-web port")
		}
		if err := nsTCP(e2eCliNS, e2ePeer2+":443", []byte("GET / HTTP/1.1\r\n\r\n")); err != nil {
			t.Fatalf("banned tunnel client lost port 443: %v", err)
		}
		if err := nsTCP(e2eCli2NS, e2ePeer2+":8080", []byte("plain")); err != nil {
			t.Fatalf("another tunnel client was cut by the first one's ban: %v", err)
		}
		_ = nsTCP(e2eCli2NS, e2ePeer2+":6890", []byte("hello"))
		if !setHas(t, "peer4", e2ePeer2+" . 6890") {
			t.Fatal("a BT answer forwarded back to a tunnel client did not teach its endpoint")
		}
		if c := m.Status().Counters; c.TunnelBans == 0 {
			t.Fatalf("counters = %+v, want a tunnel ban hit", c)
		}
	})

	t.Run("ensure keeps live sets and refreshes protected ports", func(t *testing.T) {
		if err := m.Ensure([]uint16{9443, 7000}); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if !setHas(t, "peer4", e2ePeer+" . 6890") || !setHas(t, "protected", "7000") {
			t.Fatal("Ensure on a loaded table dropped learned peers or kept stale protected ports")
		}
		e2eSh(t, "nft delete table inet "+TableName)
		if err := m.Ensure(protected); err != nil || !m.Status().Active {
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
