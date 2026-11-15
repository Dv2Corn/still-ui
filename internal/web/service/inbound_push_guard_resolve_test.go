package service

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func stubPushGuardLookup(t *testing.T, resolved map[string]string) {
	t.Helper()
	old := pushGuardLookup
	pushGuardLookup = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if ip, ok := resolved[host]; ok {
			return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	t.Cleanup(func() { pushGuardLookup = old })
}

// An inbound on another loopback address binds next to a foreign service on 127.0.0.1, so it
// makes only its own address a local fallback dest, not "<port>" or "127.0.0.1:<port>".
func TestPushedFallbackToAnotherLoopbackAddressOpensOnlyThatAddress(t *testing.T) {
	push := pushGuardTestService(t)
	seedInboundConflict(t, "loop2", "127.0.0.2", 45441, model.VLESS, `{"network":"tcp"}`, vlessSettings)
	seedInboundConflict(t, "loop6", "::1", 45442, model.VLESS, `{"network":"tcp"}`, vlessSettings)
	port := 44460
	fallback := func(dest string) error {
		port++
		_, _, err := push.AddInbound(&model.Inbound{
			Enable: true, Port: port, Protocol: model.VLESS,
			Settings: `{"clients":[],"decryption":"none","fallbacks":[{"dest":` + dest + `}]}`, StreamSettings: `{"network":"tcp"}`,
		})
		return err
	}
	for _, dest := range []string{`45441`, `"127.0.0.1:45441"`, `45442`, `"127.0.0.1:45442"`} {
		if err := fallback(dest); err == nil || !strings.Contains(err.Error(), "may not set fallback dest") {
			t.Errorf("fallback dest %s = %v, want refused: nothing of this node listens on 127.0.0.1 there", dest, err)
		}
	}
	for _, dest := range []string{`"127.0.0.2:45441"`, `"[::1]:45442"`} {
		if err := fallback(dest); err != nil {
			t.Errorf("fallback dest %s refused: %v", dest, err)
		}
	}
}

// Every pushed relay target name is resolved, on any port: a name for loopback or the LAN,
// a dead name and a zoned address are refused for a fallback and for REALITY alike.
func TestPushedRelayTargetNamesAreResolved(t *testing.T) {
	push := pushGuardTestService(t)
	stubPushGuardLookup(t, map[string]string{"www.nvidia.com": "23.215.0.136", "rebind.example.com": "127.0.0.1", "lan.example.com": "192.168.1.10", "meta.example.com": "169.254.169.254"})
	port := 44470
	add := func(reality bool, target string) error {
		port++
		ib := &model.Inbound{Enable: true, Port: port, Protocol: model.VLESS, Settings: `{"clients":[],"decryption":"none","fallbacks":[{"dest":"` + target + `"}]}`, StreamSettings: `{"network":"tcp"}`}
		if reality {
			ib.Settings = `{"clients":[]}`
			ib.StreamSettings = `{"network":"tcp","security":"reality","realitySettings":{"target":"` + target + `"}}`
		}
		_, _, err := push.AddInbound(ib)
		return err
	}
	for _, reality := range []bool{false, true} {
		field := map[bool]string{false: "fallback dest", true: "REALITY target"}[reality]
		for _, target := range []string{"rebind.example.com:5432", "lan.example.com:8080", "meta.example.com:80", "[2001:db8::5%eth0]:8080"} {
			if err := add(reality, target); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("may not set %s to %q", field, target)) {
				t.Errorf("%s %q = %v, want refused", field, target, err)
			}
		}
		if err := add(reality, "unknown.example.com:8443"); err == nil || !strings.Contains(err.Error(), field+" (its name does not resolve)") {
			t.Errorf("%s with an unresolvable name = %v, want refused as unresolvable", field, err)
		}
		if err := add(reality, "www.nvidia.com:8443"); err != nil && strings.Contains(err.Error(), "may not set") {
			t.Errorf("%s with a public name refused: %v", field, err)
		}
	}
}

// A target the node already stores is not resolved again: with the resolver down, a push
// that leaves it alone still goes through, and only a new name is refused.
func TestPushedUnchangedRelayTargetIsNotResolvedAgain(t *testing.T) {
	push := pushGuardTestService(t)
	stubPushGuardLookup(t, map[string]string{"www.nvidia.com": "23.215.0.136"})
	reality := func(target, remark string) *model.Inbound {
		return &model.Inbound{
			Enable: true, Port: 44490, Protocol: model.VLESS, Remark: remark, Settings: `{"clients":[]}`,
			StreamSettings: `{"network":"tcp","security":"reality","realitySettings":{"target":"` + target + `"}}`,
		}
	}
	stored, _, err := push.AddInbound(reality("www.nvidia.com:443", "first"))
	if err != nil {
		t.Fatalf("push the inbound: %v", err)
	}
	stubPushGuardLookup(t, nil)
	same := reality("www.nvidia.com:443", "renamed")
	same.Id, same.Tag = stored.Id, stored.Tag
	if _, _, err := push.UpdateInbound(same); err != nil {
		t.Fatalf("a push keeping the stored target, resolver down: %v", err)
	}
	moved := reality("www.microsoft.com:443", "moved")
	moved.Id, moved.Tag = stored.Id, stored.Tag
	if _, _, err := push.UpdateInbound(moved); err == nil || !strings.Contains(err.Error(), "its name does not resolve") {
		t.Fatalf("a push moving the target, resolver down = %v, want refused as unresolvable", err)
	}
}

// certbot's store is a node's own certificates as much as x-ui.sh's /root/cert; ~/.acme.sh
// also holds account keys and DNS API credentials and stays refused.
func TestPushedCertificatePathsFromCertbotAreTheNodesOwn(t *testing.T) {
	for path, own := range map[string]bool{
		"/etc/letsencrypt/live/example.com/fullchain.pem": true,
		"/etc/letsencrypt/live/example.com/privkey.pem":   true,
		"/root/cert/example.com/fullchain.pem":            true,
		"/etc/letsencrypt/live/../accounts/key.pem":       false,
		"/etc/letsencrypt/renewal/example.com.conf":       false,
		"/root/.acme.sh/account.conf":                     false,
		"/etc/shadow":                                     false,
	} {
		if got := isNodeOwnCertFile(path, nil); got != own {
			t.Errorf("isNodeOwnCertFile(%q) = %v, want %v", path, got, own)
		}
	}
}

// A fallback dials TCP. An inbound that takes only UDP on a port leaves that TCP port to
// whatever else binds it, so it makes no local fallback dest.
func TestPushedFallbackToAUDPOnlyInboundsPortIsRefused(t *testing.T) {
	push := pushGuardTestService(t)
	seedInboundConflict(t, "wg", "", 45451, model.WireGuard, "", wireguardKey)
	seedInboundConflict(t, "hy", "", 45452, model.Hysteria, hysteriaStream, hysteriaSettings)
	seedInboundConflict(t, "tcp", "", 45453, model.VLESS, `{"network":"tcp"}`, vlessSettings)
	port := 44500
	fallback := func(dest string) error {
		port++
		_, _, err := push.AddInbound(&model.Inbound{
			Enable: true, Port: port, Protocol: model.VLESS,
			Settings: `{"clients":[],"decryption":"none","fallbacks":[{"dest":` + dest + `}]}`, StreamSettings: `{"network":"tcp"}`,
		})
		return err
	}
	for _, dest := range []string{`45451`, `"127.0.0.1:45451"`, `45452`} {
		if err := fallback(dest); err == nil || !strings.Contains(err.Error(), "may not set fallback dest") {
			t.Errorf("fallback dest %s = %v, want refused: only UDP is served there", dest, err)
		}
	}
	if err := fallback(`45453`); err != nil {
		t.Errorf("fallback dest to the TCP inbound refused: %v", err)
	}
}

// One push asks the resolver once per name and gives it one time budget in all.
func TestPushedRelayTargetNamesShareOneLookupAndOneBudget(t *testing.T) {
	push := pushGuardTestService(t)
	asked := map[string]int{}
	var budget time.Duration
	old := pushGuardLookup
	pushGuardLookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		asked[host]++
		if deadline, ok := ctx.Deadline(); ok {
			budget = time.Until(deadline)
		}
		return []net.IPAddr{{IP: net.ParseIP("23.215.0.136")}}, nil
	}
	t.Cleanup(func() { pushGuardLookup = old })
	ib := &model.Inbound{
		Enable: true, Port: 44520, Protocol: model.VLESS,
		Settings:       `{"clients":[],"decryption":"none","fallbacks":[{"dest":"www.nvidia.com:8443"},{"dest":"WWW.nvidia.com:8444"},{"dest":"www.nvidia.com.:8445"}]}`,
		StreamSettings: `{"network":"tcp","security":"reality","realitySettings":{"target":"www.nvidia.com:443"}}`,
	}
	if _, _, err := push.AddInbound(ib); err != nil && strings.Contains(err.Error(), "may not set") {
		t.Fatalf("push refused: %v", err)
	}
	total := 0
	for _, n := range asked {
		total += n
	}
	if total != 1 {
		t.Fatalf("resolver asked %v, want once for the one name", asked)
	}
	if budget <= 0 || budget > pushGuardLookupBudget {
		t.Fatalf("lookup budget = %v, want within %v", budget, pushGuardLookupBudget)
	}
}

// The fallback guard reads transports as the core does: a kcp inbound written with a
// capitalised or duplicated key is UDP-only and opens no TCP fallback dest.
func TestPushedFallbackToAFoldedKeyUDPInboundIsRefused(t *testing.T) {
	push := pushGuardTestService(t)
	seedInboundConflict(t, "kcp1", "", 45461, model.VLESS, `{"Network":"kcp"}`, vlessSettings)
	seedInboundConflict(t, "kcp2", "", 45462, model.VLESS, `{"network":"tcp","Network":"kcp"}`, vlessSettings)
	port := 44530
	for _, dest := range []string{`45461`, `"127.0.0.1:45462"`} {
		port++
		_, _, err := push.AddInbound(&model.Inbound{
			Enable: true, Port: port, Protocol: model.VLESS,
			Settings: `{"clients":[],"decryption":"none","fallbacks":[{"dest":` + dest + `}]}`, StreamSettings: `{"network":"tcp"}`,
		})
		if err == nil || !strings.Contains(err.Error(), "may not set fallback dest") {
			t.Errorf("fallback dest %s = %v, want refused: only UDP is served there", dest, err)
		}
	}
}
