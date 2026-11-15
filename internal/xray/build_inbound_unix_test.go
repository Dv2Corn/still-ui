//go:build unix

package xray

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xtls/xray-core/infra/conf"
)

func buildInboundJSON(t *testing.T, raw string) error {
	t.Helper()
	detour := new(conf.InboundDetourConfig)
	if err := json.Unmarshal([]byte(raw), detour); err != nil {
		t.Fatal(err)
	}
	_, err := BuildInboundDetour(detour, []byte(raw))
	return err
}

// The loader panics on a REALITY client block with a bad spiderX; the build returns it as an error.
func TestBuildInboundDetourReturnsALoaderPanic(t *testing.T) {
	err := buildInboundJSON(t, `{"port":443,"protocol":"vless","settings":{"clients":[],"decryption":"none"},
		"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"password":"aGVsbG9oZWxsb2hlbGxvaGVsbG9oZWxsb2hlbGxvMTI","spiderX":"/%zz"}}}`)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("BuildInboundDetour = %v, want the loader panic as an error", err)
	}
}

// A certificate FIFO nested in xhttp downloadSettings, directly or in "extra", is refused
// before the build would open it and block.
func TestBuildInboundDetourRefusesANestedCertificateFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "cert.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	download := `{"address":"1.1.1.1","port":443,"network":"xhttp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"` + fifo + `","keyFile":"` + fifo + `"}]}}`
	for name, xhttp := range map[string]string{
		"downloadSettings":       `{"downloadSettings":` + download + `}`,
		"extra.downloadSettings": `{"extra":{"downloadSettings":` + download + `}}`,
	} {
		done := make(chan error, 1)
		go func() {
			done <- buildInboundJSON(t, `{"port":443,"protocol":"vless","settings":{"clients":[],"decryption":"none"},"streamSettings":{"network":"xhttp","xhttpSettings":`+xhttp+`}}`)
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Errorf("%s: BuildInboundDetour = %v, want the FIFO refused", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: the build blocked on the FIFO", name)
		}
	}
}
