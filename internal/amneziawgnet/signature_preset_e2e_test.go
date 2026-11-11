package amneziawgnet

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	wgutil "github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
)

func presetObfuscation(t *testing.T, preset string) amneziawg.Obfuscation31 {
	t.Helper()
	obf := amneziawg.GenerateObfuscation31()
	if preset != "" {
		spec, err := amneziawg.SignaturePacket(preset)
		if err != nil {
			t.Fatal(err)
		}
		obf.I1 = spec
	}
	if err := amneziawg.ValidateServerObfuscation(obf); err != nil {
		t.Fatalf("preset %q does not validate: %v", preset, err)
	}
	return obf
}

func waitForHandshake(t *testing.T, d *Device) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(deviceDump(t, d), "\n") {
			if v, ok := strings.CutPrefix(line, "last_handshake_time_sec="); ok && v != "0" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no handshake completed within 15s:\n%s", deviceDump(t, d))
}

// A real amneziawg-go client and server must finish a handshake with every preset the panel offers.
func TestSignaturePresetsCompleteAHandshake(t *testing.T) {
	for _, preset := range []string{"", amneziawg.SignaturePresetQUIC, amneziawg.SignaturePresetDNS} {
		t.Run("preset="+preset, func(t *testing.T) {
			obf := presetObfuscation(t, preset)
			tun := newPairedTunnelWithObfuscation(t, obf, obf)
			waitForHandshake(t, tun.client)
			waitForHandshake(t, tun.server)
		})
	}
}

// Switching an inbound to a preset changes only I1, which the receiver discards:
// a client still holding the previous I1 must keep connecting.
func TestSignaturePresetOnTheServerKeepsOldClientsConnecting(t *testing.T) {
	clientObf := presetObfuscation(t, "")
	serverObf := clientObf
	spec, err := amneziawg.SignaturePacket(amneziawg.SignaturePresetQUIC)
	if err != nil {
		t.Fatal(err)
	}
	serverObf.I1 = spec
	tun := newPairedTunnelWithObfuscation(t, serverObf, clientObf)
	waitForHandshake(t, tun.client)
}

// firstDatagramOfHandshake points a client at a plain UDP socket and returns what it sends first.
func firstDatagramOfHandshake(t *testing.T, obf amneziawg.Obfuscation31) []byte {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	priv, _, err := wgutil.GenerateWireguardKeypair()
	if err != nil {
		t.Fatal(err)
	}
	_, peerPub, err := wgutil.GenerateWireguardKeypair()
	if err != nil {
		t.Fatal(err)
	}
	inst := amneziawg.OutboundInstance{
		Tag:         "awg-preset-wire",
		Address:     []string{"10.213.0.2/32"},
		MTU:         1280,
		PrivateKey:  priv,
		Obfuscation: obf,
		Peers: []amneziawg.OutboundPeer{{
			PublicKey:  peerPub,
			Endpoint:   net.JoinHostPort("127.0.0.1", strconv.Itoa(pc.LocalAddr().(*net.UDPAddr).Port)),
			AllowedIPs: []string{"0.0.0.0/0"},
			KeepAlive:  1,
		}},
	}
	client, err := newUnconfiguredClientDevice(inst, DeviceOptions{})
	if err != nil {
		t.Fatalf("client device: %v", err)
	}
	defer client.Close()
	if err := client.ConfigureClient(inst, DeviceOptions{}); err != nil {
		t.Fatalf("ConfigureClient: %v", err)
	}
	buf := make([]byte, 65535)
	_ = pc.SetReadDeadline(time.Now().Add(15 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("the client sent nothing: %v", err)
	}
	return buf[:n]
}

func TestQUICPresetPutsAQUICInitialHeaderOnTheWire(t *testing.T) {
	b := firstDatagramOfHandshake(t, presetObfuscation(t, amneziawg.SignaturePresetQUIC))
	if len(b) < 1200 || len(b) > 1232 {
		t.Fatalf("datagram is %d bytes, want 1200..1232 (a QUIC Initial is padded to at least 1200)", len(b))
	}
	if b[0]&0xf0 != 0xc0 {
		t.Errorf("first byte %#02x is not a long-header Initial", b[0])
	}
	if got := [4]byte(b[1:5]); got != [4]byte{0, 0, 0, 1} {
		t.Errorf("version = %x, want QUIC v1", got)
	}
	if b[5] != 8 || b[14] != 0 || b[15] != 0 {
		t.Errorf("DCID length %d, SCID length %d, token length %d; want 8, 0, 0", b[5], b[14], b[15])
	}
	if b[16]>>6 != 1 {
		t.Fatalf("length field prefix %d is not a 2-byte varint", b[16]>>6)
	}
	if length := int(b[16]&0x3f)<<8 | int(b[17]); length != len(b)-18 {
		t.Errorf("length field says %d bytes follow, the datagram has %d", length, len(b)-18)
	}
}

func TestDNSPresetPutsAParsableDNSQueryOnTheWire(t *testing.T) {
	b := firstDatagramOfHandshake(t, presetObfuscation(t, amneziawg.SignaturePresetDNS))
	var p dnsmessage.Parser
	h, err := p.Start(b)
	if err != nil {
		t.Fatalf("not a DNS message: %v (%x)", err, b)
	}
	if h.Response || !h.RecursionDesired {
		t.Errorf("header %+v is not a recursive query", h)
	}
	questions, err := p.AllQuestions()
	if err != nil || len(questions) != 1 {
		t.Fatalf("questions = %v, err %v; want exactly one", questions, err)
	}
	q := questions[0]
	if q.Type != dnsmessage.TypeA || q.Class != dnsmessage.ClassINET || !strings.HasPrefix(q.Name.String(), "www.") {
		t.Errorf("question = %v, want an A IN query for a www name", q)
	}
	if _, err := p.AnswerHeader(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		t.Errorf("bytes follow the question: %v", err)
	}
}
