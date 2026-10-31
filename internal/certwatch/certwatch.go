// Package certwatch compares the certificate each TLS/Hysteria inbound actually answers with
// against the file it was configured from, so a renewal the core has not picked up is seen.
package certwatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// States of an inbound's certificate.
const (
	StateOK          = "ok"
	StateRenewed     = "renewed"     // the file was renewed, the core still answers with the old one
	StateExpiring    = "expiring"    // what the core answers with expires soon
	StateExpired     = "expired"     // what the core answers with has expired
	StateUnreachable = "unreachable" // no handshake with the inbound
)

// xrayReloadInterval is how often Xray rereads certificate files by itself (ocspStapling unset).
const xrayReloadInterval = time.Hour

// CertInfo identifies one certificate.
type CertInfo struct {
	Subject  string `json:"subject" example:"example.com"`
	NotAfter int64  `json:"notAfter" example:"1767225600"`
	SHA256   string `json:"sha256" example:"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"`
}

// Status is what one inbound's certificate check found.
type Status struct {
	Tag        string    `json:"tag" example:"in-443-tcp"`
	Port       int       `json:"port" example:"443"`
	Transport  string    `json:"transport" example:"tls"`
	State      string    `json:"state" example:"ok"`
	File       *CertInfo `json:"file,omitempty"`
	Served     *CertInfo `json:"served,omitempty"`
	ProbeError string    `json:"probeError,omitempty"`
	CheckedAt  int64     `json:"checkedAt" example:"1767225600"`
}

// Target is one certificate of one inbound to check.
type Target struct {
	Tag       string
	Port      int
	Addr      string
	Transport string // "tls" over TCP or "quic"
	SNI       string
	ALPN      []string
	File      string
	// ReloadAfter is how long the core may take to reread File by itself; 0 means never.
	ReloadAfter time.Duration
}

// TargetsFromConfig lists the file-based certificates of cfg's TLS inbounds; source maps a
// path in the running config back to the file the admin configured.
func TargetsFromConfig(cfg *xray.Config, source func(string) string) []Target {
	if cfg == nil {
		return nil
	}
	var targets []Target
	for _, ib := range cfg.InboundConfigs {
		var stream struct {
			Network     string `json:"network"`
			Security    string `json:"security"`
			TLSSettings struct {
				ServerName   string   `json:"serverName"`
				ALPN         []string `json:"alpn"`
				Certificates []struct {
					CertificateFile string `json:"certificateFile"`
					OneTimeLoading  bool   `json:"oneTimeLoading"`
					OcspStapling    uint64 `json:"ocspStapling"`
				} `json:"certificates"`
			} `json:"tlsSettings"`
		}
		if len(ib.StreamSettings) == 0 || json.Unmarshal(ib.StreamSettings, &stream) != nil || stream.Security != "tls" {
			continue
		}
		addr, ok := probeHost(ib.Listen)
		if !ok {
			continue
		}
		transport := "tls"
		if ib.Protocol == "hysteria" || stream.Network == "hysteria" {
			transport = "quic"
		}
		alpn := stream.TLSSettings.ALPN
		if transport == "quic" && len(alpn) == 0 {
			alpn = []string{"h3"}
		}
		for _, cert := range stream.TLSSettings.Certificates {
			if cert.CertificateFile == "" {
				continue
			}
			reload := xrayReloadInterval
			if cert.OneTimeLoading {
				reload = 0
			} else if cert.OcspStapling > 0 {
				reload = time.Duration(cert.OcspStapling) * time.Second
			}
			targets = append(targets, Target{
				Tag: ib.Tag, Port: ib.Port, Addr: net.JoinHostPort(addr, strconv.Itoa(ib.Port)),
				Transport: transport, SNI: stream.TLSSettings.ServerName, ALPN: alpn,
				File: source(cert.CertificateFile), ReloadAfter: reload,
			})
		}
	}
	return targets
}

// probeHost is the address to reach an inbound on from the panel's host.
func probeHost(listen []byte) (string, bool) {
	var host string
	if len(listen) > 0 && json.Unmarshal(listen, &host) != nil {
		return "", false
	}
	switch {
	case host == "", host == "0.0.0.0", host == "::", host == "::0":
		return "127.0.0.1", true
	case strings.HasPrefix(host, "/"), strings.HasPrefix(host, "@"):
		return "", false
	}
	return host, net.ParseIP(host) != nil
}

func describe(cert *x509.Certificate) *CertInfo {
	sum := sha256.Sum256(cert.Raw)
	subject := cert.Subject.CommonName
	if len(cert.DNSNames) > 0 {
		subject = cert.DNSNames[0]
	} else if len(cert.IPAddresses) > 0 {
		subject = cert.IPAddresses[0].String()
	}
	return &CertInfo{Subject: subject, NotAfter: cert.NotAfter.Unix(), SHA256: hex.EncodeToString(sum[:])}
}

// readLeaf parses the first certificate of a PEM file.
func readLeaf(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, errors.New("no certificate in " + path)
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

// probe completes a handshake with the inbound and returns the leaf it answered with; the
// result is only displayed, so the chain is deliberately not verified.
func probe(ctx context.Context, t Target, sni string) (*x509.Certificate, error) {
	conf := &tls.Config{ServerName: sni, NextProtos: t.ALPN, InsecureSkipVerify: true} //nolint:gosec // read-only inspection of our own inbound
	var state tls.ConnectionState
	if t.Transport == "quic" {
		conn, err := quic.DialAddr(ctx, t.Addr, conf, &quic.Config{HandshakeIdleTimeout: 5 * time.Second})
		if err != nil {
			return nil, err
		}
		state = conn.ConnectionState().TLS
		_ = conn.CloseWithError(0, "")
	} else {
		dialer := tls.Dialer{Config: conf}
		conn, err := dialer.DialContext(ctx, "tcp", t.Addr)
		if err != nil {
			return nil, err
		}
		state = conn.(*tls.Conn).ConnectionState()
		_ = conn.Close()
	}
	if len(state.PeerCertificates) == 0 {
		return nil, errors.New("no certificate in the handshake")
	}
	return state.PeerCertificates[0], nil
}

// Watcher keeps the latest status of every inbound and when each renewal was first seen.
type Watcher struct {
	checkMu     sync.Mutex // one Check at a time owns renewedAt and lastRestart
	mu          sync.Mutex // guards statuses for readers
	statuses    []Status
	renewedAt   map[string]time.Time
	lastRestart time.Time
	// Probe is swapped by tests; nil means a real handshake.
	Probe func(ctx context.Context, t Target, sni string) (*x509.Certificate, error)
}

// NewWatcher returns an empty watcher.
func NewWatcher() *Watcher { return &Watcher{renewedAt: map[string]time.Time{}} }

// restartCooldown keeps a renewal the core never takes from restarting it over and over.
const restartCooldown = 30 * time.Minute

// Check probes every target and reports whether the core has to be restarted to serve a
// renewed certificate: it did not reread the file in time, or what it serves expires within the hour.
func (w *Watcher) Check(ctx context.Context, targets []Target, now time.Time) (statuses []Status, restart bool) {
	w.checkMu.Lock()
	defer w.checkMu.Unlock()
	probeFn := w.Probe
	if probeFn == nil {
		probeFn = probe
	}
	seen := map[string]bool{}
	for _, t := range targets {
		st := Status{Tag: t.Tag, Port: t.Port, Transport: t.Transport, CheckedAt: now.Unix()}
		fileLeaf, err := readLeaf(t.File)
		if err != nil {
			st.State, st.ProbeError = StateUnreachable, "certificate file: "+err.Error()
			statuses = append(statuses, st)
			continue
		}
		st.File = describe(fileLeaf)
		sni := t.SNI
		if sni == "" && len(fileLeaf.DNSNames) > 0 {
			sni = fileLeaf.DNSNames[0]
		}
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		served, err := probeFn(pctx, t, sni)
		cancel()
		if err != nil {
			st.State, st.ProbeError = StateUnreachable, err.Error()
			statuses = append(statuses, st)
			continue
		}
		st.Served = describe(served)
		key := t.Tag + "\x00" + t.File
		renewed := !bytes.Equal(served.Raw, fileLeaf.Raw) && fileLeaf.NotAfter.After(served.NotAfter)
		switch {
		case renewed:
			seen[key] = true
			first, ok := w.renewedAt[key]
			if !ok {
				first = now
				w.renewedAt[key] = now
			}
			st.State = StateRenewed
			late := now.Sub(first) > t.ReloadAfter+2*time.Minute
			if late || served.NotAfter.Sub(now) < time.Hour {
				restart = true
			}
		case !now.Before(served.NotAfter):
			st.State = StateExpired
		case served.NotAfter.Sub(now) < expiringWithin(served):
			st.State = StateExpiring
		default:
			st.State = StateOK
		}
		statuses = append(statuses, st)
	}
	for key := range w.renewedAt {
		if !seen[key] {
			delete(w.renewedAt, key)
		}
	}
	w.mu.Lock()
	w.statuses = statuses
	w.mu.Unlock()
	if restart && now.Sub(w.lastRestart) < restartCooldown {
		restart = false
	}
	if restart {
		w.lastRestart = now
	}
	return statuses, restart
}

// expiringWithin warns a week ahead, or at a quarter of the lifetime for short-lived
// certificates (6-day ones on an IP are renewed with about two days left).
func expiringWithin(cert *x509.Certificate) time.Duration {
	quarter := cert.NotAfter.Sub(cert.NotBefore) / 4
	return min(7*24*time.Hour, quarter)
}

// Statuses returns the latest check results.
func (w *Watcher) Statuses() []Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Status(nil), w.statuses...)
}

// String renders a status for logs.
func (s Status) String() string {
	return fmt.Sprintf("%s:%d %s %s", s.Tag, s.Port, s.Transport, s.State)
}
