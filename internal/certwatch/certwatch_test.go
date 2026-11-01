package certwatch

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	return signedCert(t, key, "watch.test", notBefore, notAfter)
}

func signedCert(t *testing.T, key crypto.Signer, name string, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
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
	target := Target{
		Tag: "in-443-tcp", Port: 443, Addr: tlsInbound(t, &served), Transport: "tls",
		Files: []CertFile{{Path: writePEM(t, renewed), ReloadAfter: time.Hour}},
	}
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
	target := Target{Tag: "t", Addr: tlsInbound(t, &served), Transport: "tls", Files: []CertFile{{Path: writePEM(t, renewed), ReloadAfter: time.Hour}}}

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
			target := Target{Tag: "t", Addr: tlsInbound(t, &served), Transport: "tls", Files: []CertFile{{Path: writePEM(t, cert), ReloadAfter: time.Hour}}}
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
	target := Target{Tag: "hy", Addr: ln.Addr().String(), Transport: "quic", ALPN: []string{"h3"}, Files: []CertFile{{Path: writePEM(t, cert), ReloadAfter: time.Hour}}}

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
		inbound("ca-only", "vless", `"0.0.0.0"`, `{"network":"tcp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"/mirror/ca.pem","usage":"issue"}]}}`),
		inbound("pair", "trojan", `"0.0.0.0"`, `{"network":"tcp","security":"tls","tlsSettings":{"certificates":[`+
			`{"certificateFile":"/mirror/rsa.pem"},{"certificateFile":"/mirror/ecdsa.pem","usage":"encipherment"},{"certificateFile":"/mirror/ca.pem","usage":"verify"}]}}`),
	}}

	got := TargetsFromConfig(cfg, func(p string) string { return "/etc/ssl" + p })

	want := []Target{
		{Tag: "vless-tls", Port: 443, Addr: "127.0.0.1:443", Transport: "tls", Files: []CertFile{{"/etc/ssl/mirror/a.pem", time.Hour}}},
		{Tag: "hy", Port: 443, Addr: "10.0.0.5:443", Transport: "quic", ALPN: []string{"h3"}, Files: []CertFile{{"/etc/ssl/mirror/b.pem", 0}}},
		{Tag: "short", Port: 443, Addr: "127.0.0.1:443", Transport: "tls", Files: []CertFile{{"/etc/ssl/mirror/c.pem", 10 * time.Minute}}},
		{Tag: "pair", Port: 443, Addr: "127.0.0.1:443", Transport: "tls", Files: []CertFile{{"/etc/ssl/mirror/rsa.pem", time.Hour}, {"/etc/ssl/mirror/ecdsa.pem", time.Hour}}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d targets %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Tag != w.Tag || g.Addr != w.Addr || g.Transport != w.Transport || !slices.Equal(g.Files, w.Files) || len(g.ALPN) != len(w.ALPN) {
			t.Errorf("target %d = %+v, want %+v", i, g, w)
		}
	}
}

// Certificates an inbound holds but does not answer with (the RSA half of a pair, a CA that
// issues, another domain) read as renewals, and a oneTimeLoading core restarted every cooldown.
func TestCheckNeverRestartsForCertificatesThatAreNotRenewals(t *testing.T) {
	now := time.Now()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	served := testCert(t, now.Add(-time.Hour), now.Add(60*24*time.Hour))
	rsaHalf := signedCert(t, rsaKey, "watch.test", now.Add(-time.Hour), now.Add(90*24*time.Hour))
	otherDomain := signedCert(t, mustECKey(t), "other.test", now.Add(-time.Hour), now.Add(90*24*time.Hour))
	ca, issued := issuingCA(t, "watch.test", now)
	listen := func(name string, answer tls.Certificate, certs ...map[string]any) xray.InboundConfig {
		var cert atomic.Pointer[tls.Certificate]
		cert.Store(&answer)
		_, port, _ := net.SplitHostPort(tlsInbound(t, &cert))
		stream, _ := json.Marshal(map[string]any{"security": "tls", "tlsSettings": map[string]any{"serverName": "watch.test", "certificates": certs}})
		p, _ := strconv.Atoi(port)
		return xray.InboundConfig{Tag: name, Protocol: "vless", Port: p, Listen: json_util.RawMessage(`"127.0.0.1"`), StreamSettings: stream}
	}
	file := func(c tls.Certificate, extra ...string) map[string]any {
		m := map[string]any{"certificateFile": writePEM(t, c), "oneTimeLoading": true}
		if len(extra) == 1 {
			m["usage"] = extra[0]
		}
		return m
	}
	cfg := &xray.Config{InboundConfigs: []xray.InboundConfig{
		listen("rsa-ecdsa", served, file(rsaHalf), file(served)),
		// Xray answers with hour-long leaves it issues from the CA, even beside a file for the name.
		listen("ca", issued, file(served), file(ca, "issue")),
		listen("two-domains", served, file(served), file(otherDomain)),
	}}
	targets := TargetsFromConfig(cfg, func(p string) string { return p })
	w := NewWatcher()

	for minute := 0; minute <= 120; minute++ {
		statuses, restart := w.Check(context.Background(), targets, now.Add(time.Duration(minute)*time.Minute))
		if restart {
			t.Fatalf("restart at minute %d", minute)
		}
		for _, st := range statuses {
			if st.State != StateOK {
				t.Fatalf("minute %d: %s is %s, want ok", minute, st.Tag, st.State)
			}
		}
	}
}

func TestCheckRestartsOnceForARenewalAndNeverMoreOftenThanTheCooldown(t *testing.T) {
	now := time.Now()
	old := testCert(t, now.Add(-80*24*time.Hour), now.Add(10*24*time.Hour))
	renewed := testCert(t, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	for _, picksUp := range []bool{true, false} {
		var served atomic.Pointer[tls.Certificate]
		served.Store(&old)
		target := Target{Tag: "t", Addr: tlsInbound(t, &served), Transport: "tls", Files: []CertFile{{Path: writePEM(t, renewed)}}}
		w := NewWatcher()
		var restarts []int
		for minute := 0; minute <= 120; minute++ {
			if _, restart := w.Check(context.Background(), []Target{target}, now.Add(time.Duration(minute)*time.Minute)); restart {
				restarts = append(restarts, minute)
				if picksUp {
					served.Store(&renewed)
				}
			}
		}
		if picksUp && len(restarts) != 1 {
			t.Fatalf("core that picks the file up on restart: restarts at minutes %v, want exactly one", restarts)
		}
		for i := 1; i < len(restarts); i++ {
			if gap := time.Duration(restarts[i]-restarts[i-1]) * time.Minute; gap < restartCooldown {
				t.Fatalf("restarts at minutes %v: %v apart, under the %v cooldown", restarts, gap, restartCooldown)
			}
		}
		if !picksUp && len(restarts) < 2 {
			t.Fatalf("core that never picks the file up: restarts at minutes %v, want it retried after each cooldown", restarts)
		}
	}
}

// An inbound may answer with an inline certificate the panel cannot see; a file for another
// name that happens to expire later is not its renewal.
func TestCheckIgnoresALaterFileForAnotherName(t *testing.T) {
	now := time.Now()
	inline := testCert(t, now.Add(-time.Hour), now.Add(10*24*time.Hour))
	other := signedCert(t, mustECKey(t), "other.test", now.Add(-time.Hour), now.Add(90*24*time.Hour))
	var served atomic.Pointer[tls.Certificate]
	served.Store(&inline)
	target := Target{Tag: "t", Addr: tlsInbound(t, &served), Transport: "tls", Files: []CertFile{{Path: writePEM(t, other)}}}
	w := NewWatcher()

	for _, at := range []time.Duration{0, 3 * time.Minute, 40 * time.Minute} {
		if st, restart := w.Check(context.Background(), []Target{target}, now.Add(at)); restart || st[0].State != StateOK {
			t.Fatalf("after %v: restart=%v state=%s, want ok", at, restart, st[0].State)
		}
	}
}

func TestCheckStillSeesARenewalOfOneOfSeveralCertificates(t *testing.T) {
	now := time.Now()
	old := testCert(t, now.Add(-80*24*time.Hour), now.Add(10*24*time.Hour))
	renewed := testCert(t, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	key := mustECKey(t)
	otherDomain := signedCert(t, key, "other.test", now.Add(-time.Hour), now.Add(120*24*time.Hour))
	var served atomic.Pointer[tls.Certificate]
	served.Store(&old)
	target := Target{Tag: "t", Addr: tlsInbound(t, &served), Transport: "tls", Files: []CertFile{
		{Path: writePEM(t, otherDomain)}, {Path: writePEM(t, renewed)},
	}}
	w := NewWatcher()

	st, restart := w.Check(context.Background(), []Target{target}, now)
	if restart || st[0].State != StateRenewed || st[0].File.NotAfter != renewed.Leaf.NotAfter.Unix() {
		t.Fatalf("status %+v restart=%v, want renewed against the same-name file", st[0], restart)
	}
	if _, restart = w.Check(context.Background(), []Target{target}, now.Add(3*time.Minute)); !restart {
		t.Fatal("a oneTimeLoading core that missed the renewal was not restarted")
	}
}

// issuingCA returns a CA and a one-hour leaf for name signed by it, as Xray's usage "issue" makes.
func issuingCA(t *testing.T, name string, now time.Time) (ca, leaf tls.Certificate) {
	t.Helper()
	caKey, leafKey := mustECKey(t), mustECKey(t)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Hour),
		NotAfter: now.Add(10 * 365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, leafKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{caDER}, PrivateKey: caKey},
		tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
}

func mustECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
