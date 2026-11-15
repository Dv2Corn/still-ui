package sub

import (
	"errors"
	"net"
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

	for range network.MaxConnsPerSource {
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
		t.Fatalf("connection %d from one source was held open, want it closed at once", network.MaxConnsPerSource+1)
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
