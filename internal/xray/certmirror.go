package xray

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

// certMirrors maps each copy the unit's core reads to the file the admin configured, and
// each certificate copy to the copy of its key.
var certMirrors = struct {
	sync.Mutex
	source map[string]string
	keyOf  map[string]string
}{source: map[string]string{}, keyOf: map[string]string{}}

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
			if mirrorCertificatePair(cert) {
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

// mirrorCertificatePair points one certificate entry at copies, key first: a certificate copy
// is never newer than its key's, and an unreadable key leaves both entries on their sources.
func mirrorCertificatePair(cert map[string]any) bool {
	certSource, _ := cert["certificateFile"].(string)
	keySource, _ := cert["keyFile"].(string)
	if filepath.Dir(certSource) == certMirrorFolder() || filepath.Dir(keySource) == certMirrorFolder() {
		return false
	}
	keyMirror := ""
	if keySource != "" {
		keyMirror = certMirrorPath(keySource)
		if _, err := syncCertMirror(keySource, keyMirror); err != nil {
			logger.Warning("xray unit: cannot copy", keySource, "for the core:", err)
			return false
		}
		cert["keyFile"] = keyMirror
	}
	if certSource == "" {
		return keyMirror != ""
	}
	certMirror := certMirrorPath(certSource)
	if _, err := syncCertMirror(certSource, certMirror); err != nil {
		logger.Warning("xray unit: cannot copy", certSource, "for the core:", err)
		cert["keyFile"] = keySource
		return false
	}
	certMirrors.Lock()
	certMirrors.keyOf[certMirror] = keyMirror
	certMirrors.Unlock()
	cert["certificateFile"] = certMirror
	return true
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
	if err := makeUnitFolder(certMirrorFolder()); err != nil {
		return false, err
	}
	return true, writeFileAtomic(mirror, data, 0o640)
}

// SyncCertificateMirrors refreshes every copy from its source after a renewal, each key before
// its certificate; the core rereads the copies itself. It returns the sources whose content changed.
func SyncCertificateMirrors() []string {
	certMirrors.Lock()
	keyOf := maps.Clone(certMirrors.keyOf)
	sources := maps.Clone(certMirrors.source)
	certMirrors.Unlock()
	isKey := map[string]bool{}
	for _, key := range keyOf {
		isKey[key] = key != ""
	}
	var changed []string
	failed := map[string]bool{}
	refresh := func(mirror string) {
		source := sources[mirror]
		ok, err := syncCertMirror(source, mirror)
		if err != nil {
			logger.Warning("xray unit: cannot refresh the copy of", source, ":", err)
			failed[mirror] = true
		} else if ok {
			changed = append(changed, source)
		}
	}
	mirrors := slices.Sorted(maps.Keys(sources))
	for _, mirror := range mirrors {
		if isKey[mirror] {
			refresh(mirror)
		}
	}
	for _, mirror := range mirrors {
		if !isKey[mirror] && !failed[keyOf[mirror]] {
			refresh(mirror)
		}
	}
	return changed
}

// pruneCertMirrors removes the copies cfg no longer uses once it runs, private keys among them.
func pruneCertMirrors(cfg *Config) {
	used := map[string]bool{}
	for _, ib := range cfg.InboundConfigs {
		for _, path := range mirroredFiles(ib.StreamSettings) {
			used[path] = true
		}
	}
	certMirrors.Lock()
	for mirror := range certMirrors.source {
		if !used[mirror] {
			delete(certMirrors.source, mirror)
			delete(certMirrors.keyOf, mirror)
		}
	}
	certMirrors.Unlock()
	entries, err := os.ReadDir(certMirrorFolder())
	if err != nil {
		return
	}
	for _, entry := range entries {
		path := filepath.Join(certMirrorFolder(), entry.Name())
		// Dot files are writeFileAtomic's temporaries, possibly mid-write.
		if entry.Type().IsRegular() && !strings.HasPrefix(entry.Name(), ".") && !used[path] {
			if err := os.Remove(path); err != nil {
				logger.Warning("xray unit: cannot remove the unused copy", path, ":", err)
			}
		}
	}
}

// mirroredFiles lists the copies one inbound's TLS certificates point at.
func mirroredFiles(stream json_util.RawMessage) []string {
	var parsed struct {
		TLSSettings struct {
			Certificates []struct {
				CertificateFile string `json:"certificateFile"`
				KeyFile         string `json:"keyFile"`
			} `json:"certificates"`
		} `json:"tlsSettings"`
	}
	if len(stream) == 0 || json.Unmarshal(stream, &parsed) != nil {
		return nil
	}
	var files []string
	for _, cert := range parsed.TLSSettings.Certificates {
		for _, path := range []string{cert.CertificateFile, cert.KeyFile} {
			if path != "" && filepath.Dir(path) == certMirrorFolder() {
				files = append(files, path)
			}
		}
	}
	return files
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
