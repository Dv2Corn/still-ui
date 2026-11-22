package localdirect

import (
	"cmp"
	"net/netip"
	"slices"
	"strings"
)

// FullTunnelAllowedIPs is the peer AllowedIPs of a config that sends everything through the VPN.
const FullTunnelAllowedIPs = "0.0.0.0/0, ::/0"

var privateIPv4 = []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"}

// ipv4Range is an inclusive address range; uint64 keeps hi+1 from wrapping at 255.255.255.255.
type ipv4Range struct{ lo, hi uint64 }

func ipv4Uint(addr netip.Addr) uint64 {
	b := addr.As4()
	return uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])
}

func ipv4Ranges(cidrs []string) []ipv4Range {
	var out []ipv4Range
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		lo := ipv4Uint(prefix.Masked().Addr())
		out = append(out, ipv4Range{lo, lo + 1<<(32-prefix.Bits()) - 1})
	}
	return out
}

func mergeRanges(ranges []ipv4Range) []ipv4Range {
	sorted := slices.SortedFunc(slices.Values(ranges), func(a, b ipv4Range) int { return cmp.Compare(a.lo, b.lo) })
	var out []ipv4Range
	for _, r := range sorted {
		if n := len(out); n > 0 && r.lo <= out[n-1].hi+1 {
			out[n-1].hi = max(out[n-1].hi, r.hi)
			continue
		}
		out = append(out, r)
	}
	return out
}

func rangesToPrefixes(ranges []ipv4Range) []netip.Prefix {
	var out []netip.Prefix
	for _, r := range ranges {
		for lo := r.lo; lo <= r.hi; {
			bits := 32
			for bits > 0 && lo&(1<<(33-bits)-1) == 0 && lo+1<<(33-bits)-1 <= r.hi {
				bits--
			}
			addr := netip.AddrFrom4([4]byte{byte(lo >> 24), byte(lo >> 16), byte(lo >> 8), byte(lo)})
			out = append(out, netip.PrefixFrom(addr, bits))
			lo += 1 << (32 - bits)
		}
	}
	return out
}

// AmneziaWGTunnel returns the IPv4 prefixes a client tunnels: everything except the country's
// and private ranges, with the always-VPN IPv4 ranges put back because geoip lists them locally.
func AmneziaWGTunnel(countryCIDRs, proxyIPs []string) []netip.Prefix {
	var kept []ipv4Range
	next := uint64(0)
	for _, hole := range mergeRanges(ipv4Ranges(slices.Concat(countryCIDRs, privateIPv4))) {
		if hole.lo > next {
			kept = append(kept, ipv4Range{next, hole.lo - 1})
		}
		next = hole.hi + 1
	}
	if next <= 1<<32-1 {
		kept = append(kept, ipv4Range{next, 1<<32 - 1})
	}
	return rangesToPrefixes(mergeRanges(append(kept, ipv4Ranges(proxyIPs)...)))
}

// AmneziaWGAllowedIPs renders a peer's AllowedIPs. IPv4 DNS servers outside the tunnel are added
// so name lookups keep working, and ::/0 keeps IPv6 fully tunnelled; no tunnel means a full one.
func AmneziaWGAllowedIPs(tunnel []netip.Prefix, dns []string) string {
	if len(tunnel) == 0 {
		return FullTunnelAllowedIPs
	}
	cidrs := make([]string, 0, len(tunnel)+len(dns))
	for _, prefix := range tunnel {
		cidrs = append(cidrs, prefix.String())
	}
	for _, server := range dns {
		if addr, err := netip.ParseAddr(strings.TrimSpace(server)); err == nil && addr.Is4() {
			cidrs = append(cidrs, addr.String()+"/32")
		}
	}
	out := make([]string, 0, len(cidrs)+1)
	for _, prefix := range rangesToPrefixes(mergeRanges(ipv4Ranges(cidrs))) {
		out = append(out, prefix.String())
	}
	return strings.Join(append(out, "::/0"), ", ")
}
