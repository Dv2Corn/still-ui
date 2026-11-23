//go:build unix

package xray

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func buildInboundJSON(t *testing.T, raw string) error {
	t.Helper()
	_, err := BuildInbound([]byte(raw), nil)
	return err
}

// The loader panics on a REALITY client block with a bad spiderX; the build returns it as an error.
func TestBuildInboundReturnsALoaderPanic(t *testing.T) {
	err := buildInboundJSON(t, `{"port":443,"protocol":"vless","settings":{"clients":[],"decryption":"none"},
		"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"password":"aGVsbG9oZWxsb2hlbGxvaGVsbG9oZWxsb2hlbGxvMTI","spiderX":"/%zz"}}}`)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("BuildInbound = %v, want the loader panic as an error", err)
	}
}

// A certificate FIFO nested in xhttp downloadSettings, directly or in "extra", is refused
// before the build would open it and block.
func TestBuildInboundRefusesANestedCertificateFIFO(t *testing.T) {
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
				t.Errorf("%s: BuildInbound = %v, want the FIFO refused", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: the build blocked on the FIFO", name)
		}
	}
}

// A number a float64 cannot hold (1e400) broke the certificate check's own decoding, which
// then let the config through to a build that opened the FIFO; the core's structs skip it.
func TestCertificateCheckReadsAConfigWithAHugeNumber(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "cert.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	stream := `{"network":"tcp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"` + fifo + `","keyFile":"` + fifo + `"}]}}`
	for name, build := range map[string]func() error{
		"inbound": func() error {
			_, err := BuildInbound([]byte(`{"port":443,"protocol":"vless","zz":1e400,"settings":{"clients":[],"decryption":"none"},"streamSettings":`+stream+`}`), nil)
			return err
		},
		"subscription outbound": func() error {
			return ValidateOutboundConfig([]byte(`{"protocol":"vless","zz":1e400,"settings":{"address":"1.1.1.1","port":443,"id":"11111111-1111-1111-1111-111111111111","encryption":"none"},"streamSettings":` + stream + `}`))
		},
	} {
		done := make(chan error, 1)
		go func() { done <- build() }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Errorf("%s: build = %v, want the FIFO refused", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: the build blocked on the FIFO", name)
		}
	}
}
