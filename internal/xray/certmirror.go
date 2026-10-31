package xray

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

// certMirrors maps each copy the unit's core reads to the file the admin configured.
var certMirrors = struct {
	sync.Mutex
	source map[string]string
}{source: map[string]string{}}

func certMirrorFolder() string { return filepath.Join(config.GetXrayUnitFolder(), "certs") }

// certMirrorPath is stable per source path, so a renewed file never changes the config itself.
func certMirrorPath(source string) string {
	sum := sha256.Sum256([]byte(source))
	return filepath.Join(certMirrorFolder(), hex.EncodeToString(sum[:6])+"-"+filepath.Base(source))
}

// MirrorCertificateFiles points the TLS certificate and key files of cfg's inbounds at copies
// the unit's unprivileged core can read (keys under /root are not). A file it cannot read stays as is.
func MirrorCertificateFiles(cfg *Config) {
	if cfg == nil || !UnitMode() {
		return
	}
	for i := range cfg.InboundConfigs {
		ib := &cfg.InboundConfigs[i]
		if len(ib.StreamSettings) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(ib.StreamSettings))
		dec.UseNumber()
		var stream map[string]any
		if dec.Decode(&stream) != nil {
			continue
		}
		tls, _ := stream["tlsSettings"].(map[string]any)
		certs, _ := tls["certificates"].([]any)
		changed := false
		for _, entry := range certs {
			cert, _ := entry.(map[string]any)
			for _, key := range []string{"certificateFile", "keyFile"} {
				source, _ := cert[key].(string)
				if source == "" || filepath.Dir(source) == certMirrorFolder() {
					continue
				}
				mirror := certMirrorPath(source)
				if _, err := syncCertMirror(source, mirror); err != nil {
					logger.Warning("xray unit: cannot copy", source, "for the core:", err)
					continue
				}
				cert[key] = mirror
				changed = true
			}
		}
		if !changed {
			continue
		}
		if raw, err := json.Marshal(stream); err == nil {
			ib.StreamSettings = json_util.RawMessage(raw)
		}
	}
}

// syncCertMirror refreshes mirror from source and reports whether its content changed.
func syncCertMirror(source, mirror string) (bool, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return false, err
	}
	certMirrors.Lock()
	certMirrors.source[mirror] = source
	certMirrors.Unlock()
	if current, err := os.ReadFile(mirror); err == nil && bytes.Equal(current, data) {
		return false, nil
	}
	if err := os.MkdirAll(certMirrorFolder(), 0o750); err != nil {
		return false, err
	}
	return true, writeFileAtomic(mirror, data, 0o640)
}

// SyncCertificateMirrors refreshes every copy from its source after a renewal; the core
// rereads the copies itself. It returns the source files whose content changed.
func SyncCertificateMirrors() []string {
	certMirrors.Lock()
	pairs := make(map[string]string, len(certMirrors.source))
	for mirror, source := range certMirrors.source {
		pairs[mirror] = source
	}
	certMirrors.Unlock()
	var changed []string
	for mirror, source := range pairs {
		ok, err := syncCertMirror(source, mirror)
		if err != nil {
			logger.Warning("xray unit: cannot refresh the copy of", source, ":", err)
			continue
		}
		if ok {
			changed = append(changed, source)
		}
	}
	return changed
}

// CertificateSource returns the file the admin configured for a path in the running config.
func CertificateSource(path string) string {
	certMirrors.Lock()
	defer certMirrors.Unlock()
	if source, ok := certMirrors.source[path]; ok {
		return source
	}
	return path
}
