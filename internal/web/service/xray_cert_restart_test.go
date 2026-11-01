//go:build linux

package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/certwatch"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func selfSigned(t *testing.T, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "renew.test"},
		DNSNames: []string{"renew.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// childCoreServingStaleCertificate runs a stand-in child core whose TLS inbound still answers
// with a certificate expiring in 30 minutes, while its file holds the renewed one.
func childCoreServingStaleCertificate(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", bin)
	t.Setenv("XUI_LOG_FOLDER", t.TempDir())
	script := "#!/bin/sh\n[ \"$1\" = -version ] && { echo 'Xray 26.1.1 x'; exit 0; }\nexec sleep 120\n"
	if err := os.WriteFile(filepath.Join(bin, xray.GetBinaryName()), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	old := selfSigned(t, time.Now().Add(30*time.Minute))
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{old}})
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
	renewed := selfSigned(t, time.Now().Add(90*24*time.Hour))
	certFile := filepath.Join(t.TempDir(), "fullchain.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: renewed.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	stream, _ := json.Marshal(map[string]any{"security": "tls", "tlsSettings": map[string]any{
		"certificates": []any{map[string]any{"certificateFile": certFile}},
	}})
	port := ln.Addr().(*net.TCPAddr).Port
	process := xray.NewProcess(&xray.Config{InboundConfigs: []xray.InboundConfig{{
		Tag: "tls-in", Protocol: "vless", Port: port, Listen: json_util.RawMessage(`"127.0.0.1"`),
		StreamSettings: json_util.RawMessage(stream),
	}}})
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	previousProcess, previousResult := xrayState.snapshot()
	previousWatcher := certWatcher
	xrayState.replace(process)
	certWatcher = certwatch.NewWatcher()
	t.Cleanup(func() {
		_ = process.Stop()
		certWatcher = previousWatcher
		xrayState.mu.Lock()
		xrayState.process, xrayState.result = previousProcess, previousResult
		xrayState.mu.Unlock()
	})
}

// Restarting a child core drops every client, so a missed renewal only restarts it on request.
func TestMissedRenewalRestartsAChildCoreOnlyWithStillCertRestart(t *testing.T) {
	for _, tc := range []struct {
		setting     string
		wantRestart bool
	}{{"", false}, {"on", true}} {
		t.Run("STILL_CERT_RESTART="+tc.setting, func(t *testing.T) {
			t.Setenv("STILL_CERT_RESTART", tc.setting)
			childCoreServingStaleCertificate(t)
			restarted := false
			previous := restartXrayForCertificate
			restartXrayForCertificate = func(*XrayService) error { restarted = true; return nil }
			t.Cleanup(func() { restartXrayForCertificate = previous })

			(&XrayService{}).CheckCertificates()

			if restarted != tc.wantRestart {
				t.Fatalf("restarted = %v, want %v", restarted, tc.wantRestart)
			}
			if st := CertificateStatuses(); len(st) != 1 || st[0].State != certwatch.StateRenewed {
				t.Fatalf("certificate statuses %+v, want the inbound shown as renewed either way", st)
			}
		})
	}
}
