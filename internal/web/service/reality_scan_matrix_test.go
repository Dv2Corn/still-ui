package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const scanTestName = "target.test"

// scanTestCA signs the leaf certificates of the local TLS servers the matrix probes.
type scanTestCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newScanTestCA(t *testing.T) *scanTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "scan test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	prev := realityScanRoots
	realityScanRoots = pool
	t.Cleanup(func() { realityScanRoots = prev })
	return &scanTestCA{cert: cert, key: key}
}

func (ca *scanTestCA) leaf(t *testing.T, name string, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-30 * 24 * time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// startScanTestServer completes TLS handshakes with cfg and counts accepted connections.
func startScanTestServer(t *testing.T, cfg *tls.Config) (port int, accepted *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted = new(atomic.Int32)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_ = tls.Server(conn, cfg).Handshake()
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, accepted
}

func TestRealityProbeCheckMatrix(t *testing.T) {
	ca := newScanTestCA(t)
	farFuture := time.Now().Add(90 * 24 * time.Hour)
	good := ca.leaf(t, scanTestName, farFuture)

	type checks struct {
		feasible, tls13, h2, x25519, mlkem, certValid, chainValid, expiresSoon bool
	}
	cases := []struct {
		name       string
		cfg        *tls.Config
		want       checks
		wantReason string
	}{
		{
			name: "good",
			cfg:  &tls.Config{Certificates: []tls.Certificate{good}, NextProtos: []string{"h2", "http/1.1"}},
			want: checks{feasible: true, tls13: true, h2: true, x25519: true, mlkem: true, certValid: true, chainValid: true},
		},
		{
			name:       "TLS 1.2 only",
			cfg:        &tls.Config{Certificates: []tls.Certificate{good}, NextProtos: []string{"h2"}, MaxVersion: tls.VersionTLS12},
			want:       checks{h2: true, x25519: true, certValid: true, chainValid: true},
			wantReason: "server does not negotiate TLS 1.3",
		},
		{
			name:       "no h2",
			cfg:        &tls.Config{Certificates: []tls.Certificate{good}, NextProtos: []string{"http/1.1"}},
			want:       checks{tls13: true, x25519: true, certValid: true, chainValid: true},
			wantReason: "server does not negotiate HTTP/2 (h2)",
		},
		{
			name:       "no X25519",
			cfg:        &tls.Config{Certificates: []tls.Certificate{good}, NextProtos: []string{"h2"}, CurvePreferences: []tls.CurveID{tls.CurveP256}},
			wantReason: "TLS handshake failed",
		},
		{
			name: "X25519 without the hybrid group",
			cfg:  &tls.Config{Certificates: []tls.Certificate{good}, NextProtos: []string{"h2"}, CurvePreferences: []tls.CurveID{tls.X25519}},
			want: checks{feasible: true, tls13: true, h2: true, x25519: true, certValid: true, chainValid: true},
		},
		{
			name:       "expired certificate",
			cfg:        &tls.Config{Certificates: []tls.Certificate{ca.leaf(t, scanTestName, time.Now().Add(-24*time.Hour))}, NextProtos: []string{"h2"}},
			want:       checks{tls13: true, h2: true, x25519: true, expiresSoon: true},
			wantReason: "certificate not trusted",
		},
		{
			name: "certificate expiring within 14 days",
			cfg:  &tls.Config{Certificates: []tls.Certificate{ca.leaf(t, scanTestName, time.Now().Add(5*24*time.Hour+time.Hour))}, NextProtos: []string{"h2"}},
			want: checks{feasible: true, tls13: true, h2: true, x25519: true, mlkem: true, certValid: true, chainValid: true, expiresSoon: true},
		},
		{
			name:       "certificate for another name",
			cfg:        &tls.Config{Certificates: []tls.Certificate{ca.leaf(t, "other.test", farFuture)}, NextProtos: []string{"h2"}},
			want:       checks{tls13: true, h2: true, x25519: true, chainValid: true},
			wantReason: "certificate not trusted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port, _ := startScanTestServer(t, tc.cfg)
			res := (&ServerService{}).probeRealityAddr("127.0.0.1", port, scanTestName, 5*time.Second, 0, true)
			got := checks{res.Feasible, res.TLS13, res.H2, res.X25519, res.MLKEM768, res.CertValid, res.CertChainValid, res.CertExpiresSoon}
			if got != tc.want {
				t.Errorf("checks = %+v, want %+v (reason %q)", got, tc.want, res.Reason)
			}
			if !res.Reachable {
				t.Error("a server that accepted the connection must be reachable")
			}
			if !strings.HasPrefix(res.Reason, tc.wantReason) || (tc.wantReason == "" && res.Reason != "") {
				t.Errorf("reason = %q, want prefix %q", res.Reason, tc.wantReason)
			}
		})
	}

	t.Run("days left", func(t *testing.T) {
		port, _ := startScanTestServer(t, &tls.Config{
			Certificates: []tls.Certificate{ca.leaf(t, scanTestName, time.Now().Add(5*24*time.Hour+time.Hour))},
			NextProtos:   []string{"h2"},
		})
		res := (&ServerService{}).probeRealityAddr("127.0.0.1", port, scanTestName, 5*time.Second, 0, true)
		if res.CertDaysLeft != 5 {
			t.Errorf("CertDaysLeft = %d, want 5", res.CertDaysLeft)
		}
	})
}

// The guard resolves the name, refuses the address and never opens the connection.
func TestRealityProbeRefusesInternalDestinations(t *testing.T) {
	ca := newScanTestCA(t)
	port, accepted := startScanTestServer(t, &tls.Config{
		Certificates: []tls.Certificate{ca.leaf(t, scanTestName, time.Now().Add(90*24*time.Hour))},
		NextProtos:   []string{"h2"},
	})
	for _, host := range []string{"127.0.0.1", "localhost", "::1", "10.0.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "fe80::1"} {
		t.Run(host, func(t *testing.T) {
			res := (&ServerService{}).probeRealityAddr(host, port, scanTestName, 2*time.Second, 0, false)
			if !res.PrivateTarget {
				t.Errorf("PrivateTarget = false, want true (reason %q)", res.Reason)
			}
			if res.Reachable || res.Feasible || res.TLSVersion != "" || res.CertSubject != "" {
				t.Errorf("a refused destination leaked probe data: %+v", res)
			}
			if !strings.Contains(res.Reason, "blocked private/internal address") {
				t.Errorf("reason = %q, want the SSRF refusal", res.Reason)
			}
		})
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the refused probes opened %d connection(s) to the loopback server", n)
	}
}

func TestNeighbourScanIPs(t *testing.T) {
	got := neighbourScanIPs([]string{"203.0.113.57", "203.0.113.58", "2001:db8::1", "10.0.0.5", "100.64.0.9"})
	if len(got) != 252 {
		t.Fatalf("got %d neighbours, want 252 (254 hosts minus the server's own two)", len(got))
	}
	for _, banned := range []string{"203.0.113.0", "203.0.113.255", "203.0.113.57", "203.0.113.58"} {
		if slices.Contains(got, banned) {
			t.Errorf("neighbour list contains %s", banned)
		}
	}
	for _, ip := range got {
		if !strings.HasPrefix(ip, "203.0.113.") {
			t.Fatalf("neighbour %s is outside the server's own /24", ip)
		}
	}
	if got := neighbourScanIPs([]string{"192.168.1.10", "fd00::1"}); len(got) != 0 {
		t.Errorf("a server with only private addresses got %d neighbours, want none", len(got))
	}
}

func TestRealityBulkScanIsPaced(t *testing.T) {
	prev := realityBulkInterval
	realityBulkInterval = 30 * time.Millisecond
	t.Cleanup(func() { realityBulkInterval = prev })
	start := time.Now()
	// 127.0.0.0/29 is refused by the guard at once, so only the pacing takes time.
	if _, err := (&ServerService{}).ScanRealityTargets("127.0.0.0/29", false); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 8*30*time.Millisecond {
		t.Errorf("8 addresses were probed in %v, want at least %v", elapsed, 8*30*time.Millisecond)
	}
}

func TestNeighbourScanRunsOneAtATime(t *testing.T) {
	realityNeighbourScan.Lock()
	defer realityNeighbourScan.Unlock()
	_, err := (&ServerService{}).ScanRealityTargets("127.0.0.1:443", true)
	if err == nil || !strings.Contains(err.Error(), "a neighbour scan is already running") {
		t.Fatalf("err = %v, want the busy refusal", err)
	}
}

func TestSortRealityResultsPutsExpiringCertificatesLast(t *testing.T) {
	results := []*RealityScanResult{
		{Target: "slow-bad", LatencyMs: 10},
		{Target: "expiring", Feasible: true, CertExpiresSoon: true, LatencyMs: 5},
		{Target: "slow", Feasible: true, LatencyMs: 90},
		{Target: "fast", Feasible: true, LatencyMs: 20},
	}
	sortRealityResults(results)
	var order []string
	for _, r := range results {
		order = append(order, r.Target)
	}
	if want := []string{"fast", "slow", "expiring", "slow-bad"}; !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}
