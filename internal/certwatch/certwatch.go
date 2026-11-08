// Package certwatch compares the certificate each TLS/Hysteria inbound actually answers with
// against the file it was configured from, so a renewal the core has not picked up is seen.
package certwatch

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/mhsanaei/3x-ui/v3/internal/health"
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
	Subject   string `json:"subject" example:"example.com"`
	NotBefore int64  `json:"notBefore,omitempty" example:"1759449600"`
	NotAfter  int64  `json:"notAfter" example:"1767225600"`
	SHA256    string `json:"sha256" example:"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"`
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

// Target is one TLS inbound to check, with every certificate file it may answer with.
type Target struct {
	Tag       string
	Port      int
	Addr      string
	Transport string // "tls" over TCP or "quic"
	SNI       string
	ALPN      []string
	Files     []CertFile
	// Issuers are the CA files (usage "issue") Xray signs short-lived leaves with on the fly.
	Issuers []string
}

// CertFile is one certificate file of an inbound.
type CertFile struct {
	Path string
	// ReloadAfter is how long the core may take to reread Path by itself; 0 means never.
	ReloadAfter time.Duration
}

// TargetsFromConfig lists cfg's TLS inbounds with their file-based server certificates; source
// maps a path in the running config back to the file the admin configured.
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
					Usage           string `json:"usage"`
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
		target := Target{
			Tag: ib.Tag, Port: ib.Port, Addr: net.JoinHostPort(addr, strconv.Itoa(ib.Port)),
			Transport: transport, SNI: stream.TLSSettings.ServerName, ALPN: alpn,
		}
		for _, cert := range stream.TLSSettings.Certificates {
			if cert.CertificateFile != "" && cert.Usage == "issue" {
				target.Issuers = append(target.Issuers, source(cert.CertificateFile))
			}
			// A CA ("issue"/"verify") is never what the inbound answers with.
			if cert.CertificateFile == "" || (cert.Usage != "" && cert.Usage != "encipherment") {
				continue
			}
			reload := xrayReloadInterval
			if cert.OneTimeLoading {
				reload = 0
			} else if cert.OcspStapling > 0 {
				reload = time.Duration(cert.OcspStapling) * time.Second
			}
			target.Files = append(target.Files, CertFile{Path: source(cert.CertificateFile), ReloadAfter: reload})
		}
		if len(target.Files) > 0 {
			targets = append(targets, target)
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
	return &CertInfo{Subject: subject, NotBefore: cert.NotBefore.Unix(), NotAfter: cert.NotAfter.Unix(), SHA256: hex.EncodeToString(sum[:])}
}

// readLeaf parses the first certificate of a PEM file.
func readLeaf(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cert, err := health.ParseCertificate(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cert, nil
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

// restartCooldown keeps a renewal the core never takes from restarting it over and over;
// only a build with -tags e2e may shorten it (cooldown_e2e.go).
var restartCooldown = 30 * time.Minute

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
		files, err := readFiles(t.Files)
		if len(files) == 0 {
			st.State, st.ProbeError = StateUnreachable, "certificate file: "+err.Error()
			statuses = append(statuses, st)
			continue
		}
		st.File = describe(files[0].leaf)
		sni := t.SNI
		if sni == "" && len(files[0].leaf.DNSNames) > 0 {
			sni = files[0].leaf.DNSNames[0]
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
		match, renewal := compareServed(served, files)
		if match != nil {
			st.File = describe(match.leaf)
		} else if renewal != nil {
			st.File = describe(renewal.leaf)
		}
		issuer := issuedBy(served, t.Issuers)
		switch {
		case issuer != nil:
			// Xray reissues these hour-long leaves itself: neither a renewal nor an expiry.
			st.File, st.State = describe(issuer), StateOK
		case renewal != nil:
			key := t.Tag + "\x00" + renewal.Path
			seen[key] = true
			first, ok := w.renewedAt[key]
			if !ok {
				first = now
				w.renewedAt[key] = now
			}
			st.State = StateRenewed
			late := now.Sub(first) > renewal.ReloadAfter+2*time.Minute
			if late || served.NotAfter.Sub(now) < time.Hour {
				restart = true
			}
		case !now.Before(served.NotAfter):
			st.State = StateExpired
		case served.NotAfter.Sub(now) < health.CertWarnWithin(served.NotBefore, served.NotAfter, 7):
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

type fileLeaf struct {
	CertFile
	leaf *x509.Certificate
}

// readFiles parses every readable file; err is the first failure, for when none could be read.
func readFiles(files []CertFile) (leaves []fileLeaf, err error) {
	for _, f := range files {
		leaf, readErr := readLeaf(f.Path)
		if readErr != nil {
			err = cmp.Or(err, readErr)
			continue
		}
		leaves = append(leaves, fileLeaf{f, leaf})
	}
	return leaves, err
}

// compareServed finds the file the inbound answers with, or else the newest file renewing the
// served certificate's names: an RSA+ECDSA pair or one file per domain is not a renewal.
func compareServed(served *x509.Certificate, files []fileLeaf) (match, renewal *fileLeaf) {
	for i := range files {
		if bytes.Equal(files[i].leaf.Raw, served.Raw) {
			return &files[i], nil
		}
	}
	for i := range files {
		f := &files[i]
		if sameNames(f.leaf, served) && f.leaf.NotAfter.After(served.NotAfter) &&
			(renewal == nil || f.leaf.NotAfter.After(renewal.leaf.NotAfter)) {
			renewal = f
		}
	}
	return nil, renewal
}

// issuedBy returns the CA among issuers that signed served, or nil.
func issuedBy(served *x509.Certificate, issuers []string) *x509.Certificate {
	for _, path := range issuers {
		if ca, err := readLeaf(path); err == nil && served.CheckSignatureFrom(ca) == nil {
			return ca
		}
	}
	return nil
}

func sameNames(a, b *x509.Certificate) bool {
	names := func(c *x509.Certificate) []string {
		out := slices.Clone(c.DNSNames)
		for _, ip := range c.IPAddresses {
			out = append(out, ip.String())
		}
		if len(out) == 0 {
			out = append(out, c.Subject.CommonName)
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.Equal(names(a), names(b))
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
