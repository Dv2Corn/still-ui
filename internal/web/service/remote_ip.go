package service

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RemoteIP is the client address of a request: X-Forwarded-For or X-Real-IP count only when
// the request comes from an address in trustedProxyCIDRs.
func RemoteIP(r *http.Request) string {
	remoteIP, ok := extractTrustedIP(r.RemoteAddr)
	if !ok {
		return "unknown"
	}
	if !isTrustedProxy(remoteIP) {
		return remoteIP
	}

	realIP, hasRealIP := extractTrustedIP(r.Header.Get("X-Real-IP"))
	forwardedIP, hasForwarded := forwardedForClient(r.Header.Get("X-Forwarded-For"))
	switch {
	case hasRealIP && hasForwarded && realIP != forwardedIP:
		// A proxy rewrites at most one of the two, so the other is the client's
		// own claim; neither may pick the identity the login limiter keys on.
		return remoteIP
	case hasForwarded:
		return forwardedIP
	case hasRealIP:
		return realIP
	}
	return remoteIP
}

// forwardedForClient walks X-Forwarded-For from the proxy end: entries a client sent
// come first, so the first hop that is not a trusted proxy is the address one saw.
func forwardedForClient(xff string) (string, bool) {
	parts := strings.Split(xff, ",")
	client := ""
	for i := len(parts) - 1; i >= 0; i-- {
		ip, ok := extractTrustedIP(parts[i])
		if !ok {
			break
		}
		client = ip
		if !isTrustedProxy(ip) {
			break
		}
	}
	return client, client != ""
}

// FromTrustedProxy reports whether the request comes from an address in trustedProxyCIDRs.
func FromTrustedProxy(r *http.Request) bool {
	remoteIP, ok := extractTrustedIP(r.RemoteAddr)
	return ok && isTrustedProxy(remoteIP)
}

func isTrustedProxy(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}

	trusted := trustedProxyCIDRs()
	for value := range strings.SplitSeq(trusted, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			if prefix.Contains(addr) {
				return true
			}
			continue
		}
		if proxyIP, err := netip.ParseAddr(value); err == nil && proxyIP.Unmap() == addr.Unmap() {
			return true
		}
	}
	return false
}

func trustedProxyCIDRs() (trusted string) {
	trusted = DefaultTrustedProxyCIDRs
	defer func() {
		_ = recover()
	}()
	settingService := SettingService{}
	if value, err := settingService.GetTrustedProxyCIDRs(); err == nil && strings.TrimSpace(value) != "" {
		trusted = value
	}
	return trusted
}

func extractTrustedIP(value string) (string, bool) {
	candidate := strings.TrimSpace(value)
	if candidate == "" {
		return "", false
	}

	if ip, ok := parseIPCandidate(candidate); ok {
		return ip.String(), true
	}

	if host, _, err := net.SplitHostPort(candidate); err == nil {
		if ip, ok := parseIPCandidate(host); ok {
			return ip.String(), true
		}
	}

	if strings.Count(candidate, ":") == 1 {
		if host, _, err := net.SplitHostPort(fmt.Sprintf("[%s]", candidate)); err == nil {
			if ip, ok := parseIPCandidate(host); ok {
				return ip.String(), true
			}
		}
	}

	return "", false
}

func parseIPCandidate(value string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}
