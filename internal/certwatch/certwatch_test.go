package certwatch

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// testCert is a throwaway self-signed certificate made for this test only.
func testCert(t *testing.T, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "watch.test"},
		DNSNames:     []string{"watch.test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func writePEM(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fullchain.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tlsInbound answers TLS handshakes with whatever certificate *served holds.
func tlsInbound(t *testing.T, served *atomic.Pointer[tls.Certificate]) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return served.Load(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = conn.(*tls.Conn).Handshake(); _ = conn.Close() }()
		}
	}()
	return ln.Addr().String()
}

func TestCheckRestartsOnlyWhenTheCoreMissedARenewal(t *testing.T) {
	now := time.Now()
	old := testCert(t, now.Add(-5*24*time.Hour), now.Add(24*time.Hour))
	renewed := testCert(t, now.Add(-time.Hour), now.Add(5*24*time.Hour))
	var served atomic.Pointer[tls.Certificate]
	served.Store(&old)
	target := Target{Tag: "in-443-tcp", Port: 443, Addr: tlsInbound(t, &served), Transport: "tls",
		File: writePEM(t, renewed), ReloadAfter: time.Hour}
	w := NewWatcher()

	st, restart := w.Check(context.Background(), []Target{target}, now)
	if restart || st[0].State != StateRenewed || st[0].Served.NotAfter != old.Leaf.NotAfter.Unix() {
		t.Fatalf("right after renewal: restart=%v status=%+v, want renewed and Xray given time to reread", restart, st[0])
	}
	if _, restart = w.Check(context.Background(), []Target{target}, now.Add(30*time.Minute)); restart {
		t.Fatal("restarted before Xray's own reload interval passed")
	}
	if _, restart = w.Check(context.Background(), []Target{target}, now.Add(63*time.Minute)); !restart {
		t.Fatal("no restart after the core kept the old certificate past its reload interval")
	}
	if _, restart = w.Check(context.Background(), []Target{target}, now.Add(70*time.Minute)); restart {
		t.Fatal("restarted again within the cooldown")
	}

	served.Store(&renewed)
	st, restart = w.Check(context.Background(), []Target{target}, now.Add(71*time.Minute))
	if restart || st[0].State != StateOK || st[0].Served.SHA256 != st[0].File.SHA256 {
		t.Fatalf("after pickup: restart=%v status=%+v, want ok with matching certificates", restart, st[0])
	}
}

func TestCheckRestartsAtOnceWhenTheServedCertificateIsAboutToExpire(t *testing.T) {
	now := time.Now()
	old := testCert(t, now.Add(-6*24*time.Hour), now.Add(30*time.Minute))
	renewed := testCert(t, now.Add(-time.Hour), now.Add(6*24*time.Hour))
	var served atomic.Pointer[tls.Certificate]
	served.Store(&old)
	target := Target{Tag: "t", Addr: tlsInbound(t, &served), Transport: "tls", File: writePEM(t, renewed), ReloadAfter: time.Hour}

	if _, restart := NewWatcher().Check(context.Background(), []Target{target}, now); !restart {
		t.Fatal("a renewed certificate waited for the reload interval while the served one expires in 30 min")
	}
}

func TestCheckWarnsByLifetimeNotByAFixedWeek(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		from, to  time.Time
		wantState string
	}{
		{"6-day IP certificate, 2 days left", now.Add(-4 * 24 * time.Hour), now.Add(2 * 24 * time.Hour), StateOK},
		{"6-day IP certificate, 1 day left", now.Add(-5 * 24 * time.Hour), now.Add(24 * time.Hour), StateExpiring},
		{"90-day certificate, 30 days left", now.Add(-60 * 24 * time.Hour), now.Add(30 * 24 * time.Hour), StateOK},
		{"90-day certificate, 5 days left", now.Add(-85 * 24 * time.Hour), now.Add(5 * 24 * time.Hour), StateExpiring},
		{"expired", now.Add(-10 * 24 * time.Hour), now.Add(-time.Minute), StateExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cert := testCert(t, tc.from, tc.to)
			var served atomic.Pointer[tls.Certificate]
			served.Store(&cert)
			target := Target{Tag: "t", Addr: tlsInbound(t, &served), Transport: "tls", File: writePEM(t, cert), ReloadAfter: time.Hour}
			st, _ := NewWatcher().Check(context.Background(), []Target{target}, now)
			if st[0].State != tc.wantState {
				t.Fatalf("state %q, want %q", st[0].State, tc.wantState)
			}
		})
	}
}

func TestCheckProbesHysteriaOverQUIC(t *testing.T) {
	now := time.Now()
	cert := testCert(t, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	ln, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h3"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			go func() { <-conn.Context().Done() }()
		}
	}()
	target := Target{Tag: "hy", Addr: ln.Addr().String(), Transport: "quic", ALPN: []string{"h3"}, File: writePEM(t, cert), ReloadAfter: time.Hour}

	st, _ := NewWatcher().Check(context.Background(), []Target{target}, now)

	if st[0].State != StateOK || st[0].Served == nil || st[0].Served.SHA256 != st[0].File.SHA256 {
		t.Fatalf("status %+v, want the QUIC handshake's certificate", st[0])
	}
}

func TestTargetsFromConfigFindsFileCertificatesOnly(t *testing.T) {
	inbound := func(tag, protocol, listen, stream string) xray.InboundConfig {
		return xray.InboundConfig{Tag: tag, Protocol: protocol, Port: 443, Listen: json_util.RawMessage(listen), StreamSettings: json_util.RawMessage(stream)}
	}
	cfg := &xray.Config{InboundConfigs: []xray.InboundConfig{
		inbound("vless-tls", "vless", `"0.0.0.0"`, `{"network":"tcp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"/mirror/a.pem","keyFile":"/mirror/a.key"}]}}`),
		inbound("hy", "hysteria", `"10.0.0.5"`, `{"network":"hysteria","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"/mirror/b.pem","oneTimeLoading":true}]}}`),
		inbound("short", "trojan", `null`, `{"network":"tcp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"/mirror/c.pem","ocspStapling":600}]}}`),
		inbound("reality", "vless", `"0.0.0.0"`, `{"network":"tcp","security":"reality"}`),
		inbound("inline", "vless", `"0.0.0.0"`, `{"network":"tcp","security":"tls","tlsSettings":{"certificates":[{"certificate":["-----BEGIN"]}]}}`),
		inbound("unix", "vless", `"/run/x.sock"`, `{"network":"tcp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"/mirror/d.pem"}]}}`),
	}}

	got := TargetsFromConfig(cfg, func(p string) string { return "/etc/ssl" + p })

	want := []Target{
		{Tag: "vless-tls", Port: 443, Addr: "127.0.0.1:443", Transport: "tls", File: "/etc/ssl/mirror/a.pem", ReloadAfter: time.Hour},
		{Tag: "hy", Port: 443, Addr: "10.0.0.5:443", Transport: "quic", ALPN: []string{"h3"}, File: "/etc/ssl/mirror/b.pem"},
		{Tag: "short", Port: 443, Addr: "127.0.0.1:443", Transport: "tls", File: "/etc/ssl/mirror/c.pem", ReloadAfter: 10 * time.Minute},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d targets %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Tag != w.Tag || g.Addr != w.Addr || g.Transport != w.Transport || g.File != w.File ||
			g.ReloadAfter != w.ReloadAfter || len(g.ALPN) != len(w.ALPN) {
			t.Errorf("target %d = %+v, want %+v", i, g, w)
		}
	}
}
