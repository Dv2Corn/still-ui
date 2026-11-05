//go:build linux

package egressfilter

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The e2e test rebuilds a small network inside the current (throwaway) host: netns
// "sueg-out" plays the internet and a private LAN, "sueg-cli"/"sueg-cli2" are
// AmneziaWG kernel clients behind xui-awg7/xui-awg6, the host is the VPN server.
const (
	egOutNS  = "sueg-out"
	egCliNS  = "sueg-cli"
	egCli2NS = "sueg-cli2"
	egNet    = "192.0.2.10"     // internet
	egLAN    = "192.168.250.10" // a private network behind the server
	egCustom = "203.0.113.10"   // in the operator's custom list
	egNet6   = "2001:db8:7::10" // internet, IPv6
	egULA    = "fd00:7::10"     // unique-local, IPv6
	egHost   = "10.97.0.1"      // the server's tunnel address
	egHostUp = "192.0.2.1"      // the server's uplink address
	egCli2   = "10.97.1.2"      // the other tunnel client
	egServer = 2053             // a management port of the server
	egOther  = 2054             // another service of the server
)

const egListenScript = `
import socket, sys, threading
def tcp(fam, addr, port):
    s = socket.socket(fam); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    if fam == socket.AF_INET6: s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
    s.bind((addr, port)); s.listen(64)
    while True:
        c, _ = s.accept(); c.sendall(b"hi"); c.close()
def dns(addr):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.bind((addr, 53))
    while True:
        d, a = s.recvfrom(512); s.sendto(d, a)
for spec in sys.argv[1:]:
    kind, addr, port = spec.split(",")
    if kind == "dns":
        threading.Thread(target=dns, args=(addr,), daemon=True).start()
    else:
        fam = socket.AF_INET6 if kind == "tcp6" else socket.AF_INET
        threading.Thread(target=tcp, args=(fam, addr, int(port)), daemon=True).start()
threading.Event().wait()
`

// egProbeScript prints ok, refused or timeout for one TCP connect or DNS-like UDP round trip.
const egProbeScript = `
import socket, sys
kind, host, port = sys.argv[1], sys.argv[2], int(sys.argv[3])
try:
    if kind == "udp":
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(2)
        s.sendto(b"q", (host, port)); s.recvfrom(64)
    else:
        socket.create_connection((host, port), 2).recv(2)
    print("ok")
except ConnectionRefusedError:
    print("refused")
except OSError as e:
    print("timeout" if "timed out" in str(e) else "error:" + str(e))
`

func egSh(t *testing.T, cmd string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
	return string(out)
}

func egTopology(t *testing.T) {
	t.Helper()
	teardown := func() {
		_ = exec.Command("sh", "-c", "pkill -f 'sueg-listen[e]r' ; for n in "+egOutNS+" "+egCliNS+" "+egCli2NS+"; do ip netns del $n; done; ip link del wan7; ip link del xui-awg7; ip link del xui-awg6").Run()
	}
	teardown()
	t.Cleanup(teardown)
	if err := os.WriteFile("/tmp/sueg-listener.py", []byte(egListenScript), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/tmp/sueg-probe.py", []byte(egProbeScript), 0o644); err != nil {
		t.Fatal(err)
	}
	egSh(t, `set -e
sysctl -qw net.ipv4.ip_forward=1 net.ipv6.conf.all.forwarding=1
ip netns add `+egOutNS+`
ip link add wan7 type veth peer name eth0 netns `+egOutNS+`
ip addr add `+egHostUp+`/24 dev wan7 && ip -6 addr add 2001:db8:7::1/64 dev wan7 nodad && ip link set wan7 up
for a in `+egNet+`/24 `+egLAN+`/24 `+egCustom+`/24; do ip -n `+egOutNS+` addr add $a dev eth0; done
for a in `+egNet6+`/64 `+egULA+`/64; do ip -n `+egOutNS+` -6 addr add $a dev eth0 nodad; done
ip -n `+egOutNS+` link set eth0 up && ip -n `+egOutNS+` link set lo up
ip -n `+egOutNS+` route add default via `+egHostUp+`
ip -n `+egOutNS+` -6 route add default via 2001:db8:7::1
ip route replace 192.168.250.0/24 via `+egNet+`
ip route replace 203.0.113.0/24 via `+egNet+`
ip -6 route replace fd00:7::/64 via 2001:db8:7::10
mk() { ip netns add $1; ip link add $2 type veth peer name eth0 netns $1
  ip addr add $3.1/24 dev $2 && ip -6 addr add $4::1/64 dev $2 nodad && ip link set $2 up
  ip -n $1 addr add $3.2/24 dev eth0; ip -n $1 -6 addr add $4::2/64 dev eth0 nodad
  ip -n $1 link set eth0 up; ip -n $1 link set lo up
  ip -n $1 route add default via $3.1; ip -n $1 -6 route add default via $4::1; }
mk `+egCliNS+` xui-awg7 10.97.0 2001:db8:97
mk `+egCli2NS+` xui-awg6 10.97.1 2001:db8:98
ip netns exec `+egOutNS+` python3 /tmp/sueg-listener.py tcp,0.0.0.0,25 tcp,0.0.0.0,587 tcp,0.0.0.0,443 tcp,0.0.0.0,6667 tcp6,::,443 tcp6,::,25 dns,`+egNet+`,53 dns,`+egLAN+`,53 </dev/null >/dev/null 2>&1 &
ip netns exec `+egCli2NS+` python3 /tmp/sueg-listener.py tcp,0.0.0.0,9000 </dev/null >/dev/null 2>&1 &
python3 /tmp/sueg-listener.py tcp,0.0.0.0,`+fmt.Sprint(egServer)+` tcp,0.0.0.0,`+fmt.Sprint(egOther)+` </dev/null >/dev/null 2>&1 &
sleep 1`)
}

// probe runs from netns ns, or from the server itself when ns is empty.
func probe(t *testing.T, ns, kind, host string, port int) string {
	t.Helper()
	args := []string{"python3", "/tmp/sueg-probe.py", kind, host, fmt.Sprint(port)}
	if ns != "" {
		args = append([]string{"ip", "netns", "exec", ns}, args...)
	}
	out, _ := exec.Command(args[0], args[1:]...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

func egCounters(t *testing.T) Counters {
	t.Helper()
	return Default().Status().Counters
}

// Applies the real table to the real kernel and drives real connections through it.
func TestNftE2E(t *testing.T) {
	if os.Getenv("XUI_NFT_E2E") != "1" {
		t.Skip("set XUI_NFT_E2E=1 on a disposable Linux host (root, nft, python3)")
	}
	m := Default()
	_ = m.Remove()
	egTopology(t)
	t.Cleanup(func() { _ = m.Remove() })

	type check struct {
		name, ns, kind, host string
		port                 int
		want                 string
	}
	run := func(t *testing.T, checks []check) {
		t.Helper()
		for _, c := range checks {
			if got := probe(t, c.ns, c.kind, c.host, c.port); got != c.want {
				t.Errorf("%s: %s %s:%d from %q = %s, want %s", c.name, c.kind, c.host, c.port, c.ns, got, c.want)
			}
		}
	}
	open := []check{
		{"mail", egCliNS, "tcp", egNet, 25, "ok"},
		{"submission", egCliNS, "tcp", egNet, 587, "ok"},
		{"LAN", egCliNS, "tcp", egLAN, 443, "ok"},
		{"server port", egCliNS, "tcp", egHost, egServer, "ok"},
		{"custom address", egCliNS, "tcp", egCustom, 443, "ok"},
		{"ULA", egCliNS, "tcp", egULA, 443, "ok"},
	}
	t.Run("baseline without the table", func(t *testing.T) { run(t, open) })

	full := Spec{
		Filter:      Filter{SMTP: true, MailSubmission: true, Private: true, Server: true, Custom: []string{"203.0.113.0/24", "6667"}},
		ServerPorts: []uint16{egServer, 22},
	}
	if err := m.Apply(full); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	listing := egSh(t, "nft list table inet "+TableName)
	if err := m.Apply(full); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if again := egSh(t, "nft list table inet "+TableName); strings.Count(again, "\n") != strings.Count(listing, "\n") {
		t.Fatalf("re-applying changed the table:\n%s\n---\n%s", listing, again)
	}

	t.Run("tunnel clients are refused at once", func(t *testing.T) {
		run(t, []check{
			{"SMTP", egCliNS, "tcp", egNet, 25, "refused"},
			{"submission", egCliNS, "tcp", egNet, 587, "refused"},
			{"SMTP over IPv6", egCliNS, "tcp", egNet6, 25, "refused"},
			{"custom port", egCliNS, "tcp", egNet, 6667, "refused"},
			{"custom address", egCliNS, "tcp", egCustom, 443, "refused"},
			{"private LAN", egCliNS, "tcp", egLAN, 443, "refused"},
			{"unique-local IPv6", egCliNS, "tcp", egULA, 443, "refused"},
			{"panel port on the tunnel address", egCliNS, "tcp", egHost, egServer, "refused"},
			{"panel port on the uplink address", egCliNS, "tcp", egHostUp, egServer, "refused"},
		})
	})
	t.Run("everything else keeps working", func(t *testing.T) {
		run(t, []check{
			{"web", egCliNS, "tcp", egNet, 443, "ok"},
			{"web over IPv6", egCliNS, "tcp", egNet6, 443, "ok"},
			{"DNS", egCliNS, "udp", egNet, 53, "ok"},
			{"DNS to a resolver on a private network", egCliNS, "udp", egLAN, 53, "ok"},
			{"another service of the server", egCliNS, "tcp", egHost, egOther, "ok"},
			{"peer to peer inside the tunnel", egCliNS, "tcp", egCli2, 9000, "ok"},
			{"the server's own mail", "", "tcp", egNet, 25, "ok"},
			{"the server reaching its LAN", "", "tcp", egLAN, 443, "ok"},
			{"the server reaching a custom address", "", "tcp", egCustom, 443, "ok"},
			{"the server's own panel", "", "tcp", egHost, egServer, "ok"},
		})
	})
	c := egCounters(t)
	if c.Mail == 0 || c.Private == 0 || c.Server == 0 || c.Custom == 0 {
		t.Fatalf("counters did not count: %+v", c)
	}
	if err := m.Ensure(full); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if after := egCounters(t); after.Mail < c.Mail {
		t.Fatalf("an unchanged Ensure reloaded the table: %+v -> %+v", c, after)
	}

	t.Run("a narrower filter reopens the rest", func(t *testing.T) {
		if err := m.Ensure(Spec{Filter: Filter{SMTP: true}}); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		run(t, []check{
			{"SMTP", egCliNS, "tcp", egNet, 25, "refused"},
			{"submission", egCliNS, "tcp", egNet, 587, "ok"},
			{"LAN", egCliNS, "tcp", egLAN, 443, "ok"},
			{"server port", egCliNS, "tcp", egHost, egServer, "ok"},
		})
	})

	t.Run("remove is clean and idempotent", func(t *testing.T) {
		if err := m.Remove(); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if err := m.Remove(); err != nil {
			t.Fatalf("second Remove: %v", err)
		}
		if out, err := exec.Command("nft", "list", "table", "inet", TableName).CombinedOutput(); err == nil {
			t.Fatalf("table still present:\n%s", out)
		}
		run(t, open)
	})
}

// Every combination of switches renders a ruleset the kernel's nft accepts.
func TestNftCheckEveryCombination(t *testing.T) {
	if os.Getenv("XUI_NFT_E2E") != "1" {
		t.Skip("set XUI_NFT_E2E=1 on a disposable Linux host (root, nft)")
	}
	for mask := 1; mask < 32; mask++ {
		f := Filter{SMTP: mask&1 != 0, MailSubmission: mask&2 != 0, Private: mask&4 != 0, Server: mask&8 != 0}
		if mask&16 != 0 {
			f.Custom = []string{"203.0.113.0/24", "2001:db8::/32", "6660-6670", "8"}
		}
		spec := Spec{Filter: f, ServerPorts: []uint16{22, 2053}}
		if !spec.Needed() {
			continue
		}
		tx, err := Ruleset(spec)
		if err != nil {
			t.Fatalf("%+v: %v", f, err)
		}
		cmd := exec.Command("nft", "-c", "-f", "-")
		cmd.Stdin = strings.NewReader(tx)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("nft -c refused %+v: %v\n%s\n%s", f, err, out, tx)
		}
	}
}
