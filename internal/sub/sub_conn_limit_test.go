package sub

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/network"
)

// The public subscription listener shares the process with the panel, so one source must not
// hold more connections open than the panel's own listener lets it: the one past the cap is closed.
func TestSubServerCapsConnectionsPerSource(t *testing.T) {
	source := nonLoopbackIPv4(t)
	initSubDB(t)
	for key, value := range map[string]string{"subPort": "0", "subListen": source.String()} {
		if err := database.GetDB().Create(&model.Setting{Key: key, Value: value}).Error; err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	server := NewServer()
	if err := server.Start(); err != nil {
		t.Fatalf("start subscription server: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	target := net.JoinHostPort(source.String(), strconv.Itoa(server.listener.Addr().(*net.TCPAddr).Port))

	for range network.MaxSubConnsPerSource {
		conn, err := net.Dial("tcp", target)
		if err != nil {
			t.Fatalf("dial within the cap: %v", err)
		}
		t.Cleanup(func() { conn.Close() })
	}
	extra, err := net.Dial("tcp", target)
	if err != nil {
		t.Fatalf("dial past the cap: %v", err)
	}
	defer extra.Close()
	// An accepted connection waits up to ReadHeaderTimeout (5s) for a request; a refused one ends now.
	_ = extra.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := extra.Read(make([]byte, 1)); errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("connection %d from one source was held open, want it closed at once", network.MaxSubConnsPerSource+1)
	}
}

// nonLoopbackIPv4 is a local address whose connections count per source; loopback counts only overall.
func nonLoopbackIPv4(t *testing.T) net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("interface addresses: %v", err)
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil && !ipNet.IP.IsLoopback() && !ipNet.IP.IsLinkLocalUnicast() {
			return ipNet.IP.To4()
		}
	}
	t.Skip("no non-loopback IPv4 address to connect from")
	return nil
}

// The listener's wiring: a front proxy named in trustedProxyCIDRs has no per-source share,
// any other address has one, and an idle connection is closed after subIdleTimeout.
func TestSubServerTrustsTheConfiguredFrontAndClosesIdleConnections(t *testing.T) {
	initSubDB(t)
	for key, value := range map[string]string{"subPort": "0", "subListen": "127.0.0.1", "trustedProxyCIDRs": "127.0.0.1/32,203.0.113.0/24"} {
		if err := database.GetDB().Create(&model.Setting{Key: key, Value: value}).Error; err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	server := NewServer()
	front := server.trustedFront()
	for addr, want := range map[string]bool{"203.0.113.9": true, "198.51.100.1": false, "2001:db8::1": false} {
		if got := front(netip.MustParseAddr(addr)); got != want {
			t.Errorf("trustedFront(%s) = %v, want %v", addr, got, want)
		}
	}
	if err := server.Start(); err != nil {
		t.Fatalf("start subscription server: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	if got := server.httpServer.IdleTimeout; got != subIdleTimeout || got > 15*time.Second {
		t.Fatalf("idle timeout = %v, want %v", got, subIdleTimeout)
	}
}
