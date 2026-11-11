package amneziawg

import (
	"encoding/hex"
	"fmt"
)

// Signature packet presets: an I1 shaped like the first datagram of another UDP protocol.
const (
	SignaturePresetQUIC = "quic"
	SignaturePresetDNS  = "dns"
)

// A QUIC Initial is at least 1200 bytes (RFC 9000 §14.1); 1232 still fits IPv6's 1280-byte minimum MTU.
const (
	quicInitialMin = 1200
	quicInitialMax = 1232
	// quicInitialHeader is flags, version, DCID length + 8-byte DCID, SCID length, token length, 2-byte length.
	quicInitialHeader = 1 + 4 + 1 + 8 + 1 + 1 + 2
)

// dnsPresetNames are queried by the DNS-like packet; one is picked per generated preset.
var dnsPresetNames = []string{
	"www.google.com", "www.microsoft.com", "www.apple.com", "www.amazon.com", "www.cloudflare.com",
}

// SignaturePacket builds the I1 spec of a preset. The parts a real sender randomises
// (connection id, query id, encrypted payload) are <r N>, so every handshake differs.
func SignaturePacket(preset string) (string, error) {
	switch preset {
	case SignaturePresetQUIC:
		payload := randInt(quicInitialMin, quicInitialMax) - quicInitialHeader
		// Long header, fixed bit, type Initial; the low four bits are header-protected, so random.
		flags := 0xc0 | randInt(0, 15)
		return fmt.Sprintf("<b 0x%02x0000000108><r 8><b 0x0000%04x><r %d>", flags, 0x4000|payload, payload), nil
	case SignaturePresetDNS:
		name := dnsPresetNames[randInt(0, len(dnsPresetNames)-1)]
		// Standard query, recursion desired, one question: <name> A IN.
		return "<r 2><b 0x01000001000000000000" + hex.EncodeToString(dnsQName(name)) + "00010001>", nil
	}
	return "", fmt.Errorf("unknown signature packet preset %q", preset)
}

func dnsQName(name string) []byte {
	var out []byte
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			out = append(out, byte(i-start))
			out = append(out, name[start:i]...)
			start = i + 1
		}
	}
	return append(out, 0)
}
