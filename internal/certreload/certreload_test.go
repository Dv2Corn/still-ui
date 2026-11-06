package certreload

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type keyPEM struct{ cert, key []byte }

func newPair(t *testing.T, serial int64, notAfter time.Time) keyPEM {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "panel.example.com"},
		DNSNames:     []string{"panel.example.com"},
		NotBefore:    notAfter.Add(-6 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return keyPEM{
		cert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		key:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}
}

// write puts data in path with a modification time of its own, as a renewal does.
func write(t *testing.T, path string, data []byte, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func serialOf(t *testing.T, c *tls.Certificate) int64 {
	t.Helper()
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.Int64()
}

func TestPairServesARenewalWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	t0 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	old, renewed := newPair(t, 1, t0.Add(24*time.Hour)), newPair(t, 2, t0.Add(7*24*time.Hour))
	write(t, certFile, old.cert, t0)
	write(t, keyFile, old.key, t0)

	p, err := Load(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	clock := t0
	p.now = func() time.Time { return clock }
	p.checked = clock

	// acme.sh writes the certificate first: with the old key the pair does not load yet.
	write(t, certFile, renewed.cert, t0.Add(time.Minute))
	clock = clock.Add(checkEvery)
	if c, _ := p.GetCertificate(nil); serialOf(t, c) != 1 {
		t.Fatalf("a half-written renewal is served (serial %d), want the old pair", serialOf(t, c))
	}
	write(t, keyFile, renewed.key, t0.Add(time.Minute+time.Second))
	clock = clock.Add(checkEvery / 2)
	if c, _ := p.GetCertificate(nil); serialOf(t, c) != 1 {
		t.Fatal("the files were read again before checkEvery passed")
	}
	clock = clock.Add(checkEvery)
	if c, _ := p.GetCertificate(nil); serialOf(t, c) != 2 {
		t.Fatalf("serial %d served after the renewal, want 2", serialOf(t, c))
	}
	if got := p.Served().NotAfter; !got.Equal(t0.Add(7 * 24 * time.Hour)) {
		t.Fatalf("Served().NotAfter = %v, want the renewed certificate's", got)
	}

	// A broken file later on keeps the working pair.
	write(t, certFile, []byte("garbage"), t0.Add(time.Hour))
	clock = clock.Add(checkEvery)
	if c, _ := p.GetCertificate(nil); serialOf(t, c) != 2 {
		t.Fatal("an unreadable certificate file replaced the working pair")
	}
}

func TestPairOverTLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	t0 := time.Now()
	first, second := newPair(t, 10, t0.Add(48*time.Hour)), newPair(t, 11, t0.Add(96*time.Hour))
	write(t, certFile, first.cert, t0.Add(-time.Hour))
	write(t, keyFile, first.key, t0.Add(-time.Hour))
	p, err := Load(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	clock := t0
	p.now = func() time.Time { return clock }
	p.checked = clock

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{GetCertificate: p.GetCertificate})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()
	servedSerial := func() int64 {
		t.Helper()
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", ln.Addr().String(),
			&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // the test reads what is served
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
	}
	if got := servedSerial(); got != 10 {
		t.Fatalf("served serial %d, want 10", got)
	}
	write(t, certFile, second.cert, t0)
	write(t, keyFile, second.key, t0)
	clock = clock.Add(checkEvery)
	if got := servedSerial(); got != 11 {
		t.Fatalf("after renewal served serial %d, want 11", got)
	}
}
