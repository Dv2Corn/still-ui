// Package certreload serves a TLS key pair from its files and picks up a renewed pair by
// itself, so the panel and the subscription server need no restart after acme.sh renews.
package certreload

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// checkEvery is how often a handshake may look at the files: a renewed certificate is
// served at most this long after it was written.
const checkEvery = 30 * time.Second

type stamp struct {
	certMod, keyMod   time.Time
	certSize, keySize int64
}

func stampOf(certFile, keyFile string) (stamp, bool) {
	c, err := os.Stat(certFile)
	if err != nil {
		return stamp{}, false
	}
	k, err := os.Stat(keyFile)
	if err != nil {
		return stamp{}, false
	}
	return stamp{certMod: c.ModTime(), keyMod: k.ModTime(), certSize: c.Size(), keySize: k.Size()}, true
}

// Pair is a key pair loaded from certFile and keyFile.
type Pair struct {
	certFile, keyFile string
	now               func() time.Time

	mu      sync.Mutex
	cert    *tls.Certificate
	loaded  stamp // the files the served certificate came from
	refused stamp // files that did not load: not retried until they change again
	checked time.Time
}

// Load reads the pair; it fails as tls.LoadX509KeyPair does.
func Load(certFile, keyFile string) (*Pair, error) {
	st, _ := stampOf(certFile, keyFile)
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &Pair{certFile: certFile, keyFile: keyFile, now: time.Now, cert: &cert, loaded: st, checked: time.Now()}, nil
}

// GetCertificate is the tls.Config hook: the current pair, reloaded first when its files changed.
func (p *Pair) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reloadLocked()
	return p.cert, nil
}

// Served is the certificate handshakes get now.
func (p *Pair) Served() *x509.Certificate {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reloadLocked()
	return p.cert.Leaf
}

func (p *Pair) reloadLocked() {
	now := p.now()
	if now.Sub(p.checked) < checkEvery {
		return
	}
	p.checked = now
	st, ok := stampOf(p.certFile, p.keyFile)
	if !ok || st == p.loaded || st == p.refused {
		return
	}
	// acme.sh writes the key and the certificate one after the other: a half-written
	// pair does not load, and the next change of either file is tried again.
	cert, err := tls.LoadX509KeyPair(p.certFile, p.keyFile)
	if err != nil {
		p.refused = st
		logger.Warning("Keeping the certificate in use; the changed files", p.certFile, "do not load:", err)
		return
	}
	p.cert, p.loaded, p.refused = &cert, st, stamp{}
	logger.Info("Serving the renewed certificate from", p.certFile)
}

var (
	servedMu sync.Mutex
	served   = map[string]*Pair{}
)

// Register records the pair a listener serves under role ("panel", "subscription"); nil
// records that it serves none (plain HTTP or no listener).
func Register(role string, p *Pair) {
	servedMu.Lock()
	defer servedMu.Unlock()
	if p == nil {
		delete(served, role)
		return
	}
	served[role] = p
}

// Served returns the certificate the listener of role serves, and its file, if it serves one.
func Served(role string) (*x509.Certificate, string, bool) {
	servedMu.Lock()
	p := served[role]
	servedMu.Unlock()
	if p == nil {
		return nil, "", false
	}
	return p.Served(), p.certFile, true
}
