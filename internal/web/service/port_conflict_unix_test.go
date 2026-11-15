//go:build unix

package service

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Certificate files nested in xhttp downloadSettings, also inside "extra", are read by the
// build; a FIFO there must not block the panel (stripped, or refused before the build).
func TestConfigTransportsDoesNotBlockOnANestedCertificateFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "cert.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	download := `{"address":"1.1.1.1","port":443,"network":"xhttp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"` + fifo + `","keyFile":"` + fifo + `"}]}}`
	for name, stream := range map[string]string{
		"downloadSettings":       `{"network":"xhttp","xhttpSettings":{"downloadSettings":` + download + `}}`,
		"extra.downloadSettings": `{"network":"xhttp","xhttpSettings":{"extra":{"downloadSettings":` + download + `}}}`,
	} {
		done := make(chan struct{})
		go func() {
			transportsOf(model.VLESS, stream, vlessSettings)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("%s: the build blocked on the FIFO", name)
		}
	}
}
