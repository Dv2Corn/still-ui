package network

import (
	"net"
	"net/netip"
	"sync"
)

// Every open panel or subscription connection holds buffers and a goroutine before any login,
// so their number is bounded overall and per source; a loopback reverse proxy counts only overall.
const (
	MaxConns          = 2048
	MaxConnsPerSource = 256
	// A subscription client fetches once and leaves, so its share is smaller: filling the
	// total then takes 32 sources, not 8.
	MaxSubConnsPerSource = 64
)

type limitListener struct {
	net.Listener
	total, perSource int
	// uncounted names further sources without a share of their own, such as a trusted L4
	// front on another host, whose address every client behind it shares.
	uncounted func(netip.Addr) bool

	mu       sync.Mutex
	open     int
	bySource map[netip.Prefix]int
}

// NewLimitListener closes an accepted connection at once when the total or its source
// (an IPv4 address or IPv6 /64) is at its limit, rather than leaving it in the backlog.
func NewLimitListener(l net.Listener, total, perSource int, uncounted func(netip.Addr) bool) net.Listener {
	return &limitListener{Listener: l, total: total, perSource: perSource, uncounted: uncounted, bySource: map[netip.Prefix]int{}}
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		source, counted := connSource(c.RemoteAddr(), l.uncounted)
		if !l.acquire(source, counted) {
			_ = c.Close()
			continue
		}
		return &limitConn{Conn: c, release: func() { l.release(source, counted) }}, nil
	}
}

func (l *limitListener) acquire(source netip.Prefix, counted bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open >= l.total || (counted && l.bySource[source] >= l.perSource) {
		return false
	}
	l.open++
	if counted {
		l.bySource[source]++
	}
	return true
}

func (l *limitListener) release(source netip.Prefix, counted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.open--
	if counted {
		if l.bySource[source]--; l.bySource[source] <= 0 {
			delete(l.bySource, source)
		}
	}
}

// connSource is the per-source bucket of addr; loopback, uncounted and unparseable sources have none.
func connSource(addr net.Addr, uncounted func(netip.Addr) bool) (netip.Prefix, bool) {
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return netip.Prefix{}, false
	}
	ip := ap.Addr().Unmap()
	if ip.IsLoopback() || (uncounted != nil && uncounted(ip)) {
		return netip.Prefix{}, false
	}
	bits := 32
	if ip.Is6() {
		bits = 64
	}
	prefix, err := ip.Prefix(bits)
	return prefix, err == nil
}

type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
