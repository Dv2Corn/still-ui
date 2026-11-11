// Package connlog keeps the connection log: which client connected, when, through which
// inbound and where to. It reads Xray's access log and stores one SQLite file per day.
package connlog

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// What is kept of a destination and of a client's own address.
const (
	DestinationFull   = "full"
	DestinationDomain = "domain"
	DestinationNone   = "none"

	SourceFull   = "full"
	SourceMasked = "masked"
	SourceNone   = "none"
)

// Policy is the operator's choice of what a stored connection may reveal.
type Policy struct {
	Destination string
	Source      string
}

// ConnRecord is one accepted connection.
type ConnRecord struct {
	// Time is unix milliseconds.
	Time     int64  `json:"time" example:"1735732800000"`
	Client   string `json:"client" example:"alice@example.com"`
	Inbound  string `json:"inbound" example:"inbound-443"`
	Outbound string `json:"outbound" example:"direct"`
	// Source is the client's address, or its /24 (IPv4) or /48 (IPv6) network when masked.
	Source     string `json:"source" example:"203.0.113.9"`
	SourcePort int    `json:"sourcePort" example:"54321"`
	Network    string `json:"network" example:"tcp"`
	Host       string `json:"host" example:"example.com"`
	Port       int    `json:"port" example:"443"`
}

const maxFieldBytes = 255

// recordFrom applies the policy to an access-log line; ok is false for a refused
// connection and for the panel's own calls to the Xray API.
func recordFrom(line xray.AccessLogLine, policy Policy) (ConnRecord, bool) {
	if !line.Accepted || line.Time.IsZero() || (line.Inbound == "api" && line.Outbound == "api") {
		return ConnRecord{}, false
	}
	rec := ConnRecord{
		Time:     line.Time.UnixMilli(),
		Client:   clip(line.Email),
		Inbound:  clip(line.Inbound),
		Outbound: clip(line.Outbound),
	}
	rec.Source, rec.SourcePort = source(line.From, policy.Source)
	rec.Network, rec.Host, rec.Port = destination(line.To)
	// An unknown policy value keeps nothing rather than everything.
	switch policy.Destination {
	case DestinationFull:
	case DestinationDomain:
		rec.Host = registrableDomain(rec.Host)
	default:
		rec.Host, rec.Port = "", 0
	}
	return rec, true
}

func clip(s string) string {
	if len(s) > maxFieldBytes {
		return s[:maxFieldBytes]
	}
	return s
}

func cutNetwork(addr string) (network, rest string) {
	for _, n := range []string{"tcp", "udp"} {
		if after, ok := strings.CutPrefix(addr, n+":"); ok {
			return n, after
		}
	}
	return "tcp", addr
}

// destination splits "tcp:example.com:443", "udp:[2001:db8::1]:53" and the whole URL
// Xray's HTTP inbound logs; of a URL only the host and the port are kept.
func destination(to string) (network, host string, port int) {
	if strings.Contains(to, "://") {
		if u, err := url.Parse(to); err == nil && u.Hostname() != "" {
			port, _ = strconv.Atoi(u.Port())
			if port == 0 && u.Scheme == "https" {
				port = 443
			} else if port == 0 {
				port = 80
			}
			return "tcp", clip(strings.ToLower(u.Hostname())), port
		}
	}
	network, rest := cutNetwork(to)
	host = rest
	if i := strings.LastIndexByte(rest, ':'); i >= 0 {
		if p, err := strconv.Atoi(rest[i+1:]); err == nil && p >= 0 && p <= 65535 {
			host, port = rest[:i], p
		}
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return network, clip(strings.ToLower(host)), port
}

// registrableDomain reduces a name to the part its owner registered (maps.example.co.uk
// becomes example.co.uk). An IP address or a name with no known suffix stays as it is.
func registrableDomain(host string) string {
	if _, err := netip.ParseAddr(host); err == nil {
		return host
	}
	if domain, err := publicsuffix.EffectiveTLDPlusOne(strings.TrimSuffix(host, ".")); err == nil {
		return domain
	}
	return host
}

// source returns what the policy keeps of a client address. A masked address has no
// port, and one that is not an IP address cannot be masked, so it is dropped.
func source(from, mode string) (string, int) {
	if mode != SourceFull && mode != SourceMasked {
		return "", 0
	}
	_, from = cutNetwork(from)
	addrPort, err := netip.ParseAddrPort(from)
	if err != nil {
		if mode == SourceMasked {
			return "", 0
		}
		return clip(from), 0
	}
	addr := addrPort.Addr().Unmap()
	if mode == SourceFull {
		return addr.String(), int(addrPort.Port())
	}
	bits := 48
	if addr.Is4() {
		bits = 24
	}
	prefix, _ := addr.Prefix(bits)
	return prefix.String(), 0
}
