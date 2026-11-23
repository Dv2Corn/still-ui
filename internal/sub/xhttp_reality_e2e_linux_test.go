//go:build linux

package sub

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"golang.org/x/net/proxy"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/link"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const xhttpE2ESubID = "xhttp-e2e-sub"

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// xhttpE2ECoverSite is the REALITY target: a TLS 1.3 site with HTTP/2, as Find Targets demands.
func xhttpE2ECoverSite(t *testing.T) string {
	t.Helper()
	certPEM, keyPEM, _ := hopE2ECertificate(t)
	cert, err := tls.X509KeyPair([]byte(strings.Join(certPEM, "\n")), []byte(strings.Join(keyPEM, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// REALITY drops its probe handshakes to the target; their errors are expected here.
	site := &http.Server{
		Handler:  http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "cover") }),
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() { _ = site.Serve(ln) }()
	t.Cleanup(func() { _ = site.Close() })
	return ln.Addr().String()
}

func socksGet(socks, target string) (string, error) {
	dialer, err := proxy.SOCKS5("tcp", socks, nil, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.(proxy.ContextDialer).DialContext(ctx, network, addr)
		},
	}}
	resp, err := client.Get(target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// TestXhttpRealityE2E saves the inbound the "VLESS XHTTP REALITY" preset builds, runs it as the panel's
// config generator emits it, and fetches a page through it with the JSON subscription and the share link.
func TestXhttpRealityE2E(t *testing.T) {
	bin := os.Getenv("XRAY_E2E_BINARY")
	if bin == "" {
		t.Skip("set XRAY_E2E_BINARY to still-ui's xray (scripts/xray-build)")
	}
	seedSubDB(t)
	cover := xhttpE2ECoverSite(t)
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	path := "/e2e" + b64(priv.PublicKey().Bytes()[:6])
	// Mirrors frontend/src/lib/xray/inbound-presets.ts (ruMobileXhttp) with the target filled in.
	stream := map[string]any{
		"network":  "xhttp",
		"security": "reality",
		"xhttpSettings": map[string]any{
			"path": path, "host": "", "mode": "auto", "xPaddingBytes": "100-1000",
			"scMaxBufferedPosts": 30, "scStreamUpServerSecs": "20-80", "headers": map[string]any{},
		},
		"realitySettings": map[string]any{
			"show": false, "xver": 0, "target": cover, "serverNames": []string{"www.example.test"},
			"privateKey": b64(priv.Bytes()), "shortIds": []string{"a1b2c3d4"},
			"settings": map[string]any{
				"publicKey": b64(priv.PublicKey().Bytes()), "fingerprint": "ios",
				"serverName": "", "spiderX": "/e2e",
			},
		},
	}
	streamJSON, _ := json.Marshal(stream)
	const uuid = "8b6f1a1e-6d39-4b0c-9a5e-3c1f0d1c2b3a"
	inbound, _, err := (&service.InboundService{}).AddInbound(&model.Inbound{
		UserId: 1, Enable: true, Remark: "xhttp-e2e", Listen: "127.0.0.1", Port: freeLoopbackPort(t), Protocol: model.VLESS,
		Settings:       `{"decryption":"none","clients":[{"id":"` + uuid + `","flow":"","email":"x@e2e","subId":"` + xhttpE2ESubID + `","enable":true}]}`,
		StreamSettings: string(streamJSON),
	})
	if err != nil {
		t.Fatalf("AddInbound: %v", err)
	}

	panelConfig, err := (&service.XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	var serverInbound any
	for i := range panelConfig.InboundConfigs {
		if panelConfig.InboundConfigs[i].Tag == inbound.Tag {
			serverInbound = panelConfig.InboundConfigs[i]
		}
	}
	if serverInbound == nil {
		t.Fatalf("the panel config has no inbound tagged %q", inbound.Tag)
	}
	dir := t.TempDir()
	hopE2EStartXray(t, bin, dir, "server", map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"inbounds": []any{serverInbound},
		// The origin is on loopback, which freedom refuses by default.
		"outbounds": []any{map[string]any{
			"protocol": "freedom", "tag": "direct",
			"settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow"}}},
		}},
	})

	origin := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "xhttp-ok") })}
	originLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = origin.Serve(originLn) }()
	t.Cleanup(func() { _ = origin.Close() })
	page := "http://" + originLn.Addr().String() + "/"

	links, _, _, _, err := NewSubService("").GetSubs(xhttpE2ESubID, "127.0.0.1")
	if err != nil || len(links) != 1 {
		t.Fatalf("GetSubs: %d links, %v", len(links), err)
	}
	share, err := url.Parse(links[0])
	if err != nil {
		t.Fatal(err)
	}
	q := share.Query()
	if q.Get("type") != "xhttp" || q.Get("path") != path || q.Get("fp") != "ios" || q.Get("flow") != "" {
		t.Fatalf("share link lacks the preset's transport or carries a flow: %s", links[0])
	}
	parsed, err := link.ParseLink(links[0])
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}

	body, _, err := NewSubJsonService("", "", "", "", NewSubService("")).GetJson(xhttpE2ESubID, "127.0.0.1", false)
	if err != nil {
		t.Fatalf("GetJson: %v", err)
	}
	var fromJSON map[string]any
	if err := json.Unmarshal([]byte(body), &fromJSON); err != nil {
		t.Fatalf("subscription is not one JSON config: %v\n%s", err, body)
	}

	clients := map[string]map[string]any{
		"json-subscription": fromJSON,
		// The reach probes and outbound import read share links through link.ParseLink.
		"share-link": {"outbounds": []any{map[string]any(parsed.Outbound)}},
	}
	for name, config := range clients {
		t.Run(name, func(t *testing.T) {
			socks := freeLoopbackPort(t)
			config["log"] = map[string]any{"loglevel": "warning"}
			config["inbounds"] = []any{map[string]any{
				"listen": "127.0.0.1", "port": socks, "protocol": "socks", "settings": map[string]any{"udp": false},
			}}
			delete(config, "routing")
			hopE2EStartXray(t, bin, dir, name, config)
			addr := fmt.Sprintf("127.0.0.1:%d", socks)
			waitUntil(t, 20*time.Second, "a page through "+name, func() error {
				return xhttpE2EFetch(addr, page)
			})
		})
	}

	t.Run("clash-subscription", func(t *testing.T) {
		mihomo := os.Getenv("MIHOMO_E2E_BINARY")
		if mihomo == "" {
			t.Skip("set MIHOMO_E2E_BINARY to a mihomo binary (1.19.22 or newer reads xhttp)")
		}
		profile, _, err := NewSubClashService(false, "", false, NewSubService("")).GetClash(xhttpE2ESubID, "127.0.0.1")
		if err != nil {
			t.Fatalf("GetClash: %v", err)
		}
		var config map[string]any
		if err := yaml.Unmarshal([]byte(profile), &config); err != nil {
			t.Fatalf("the Clash profile is not YAML: %v\n%s", err, profile)
		}
		if !strings.Contains(profile, "network: xhttp") {
			t.Fatalf("the Clash profile has no xhttp proxy:\n%s", profile)
		}
		port := freeLoopbackPort(t)
		config["mixed-port"] = port
		config["bind-address"] = "127.0.0.1"
		home := t.TempDir()
		raw, err := yaml.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "config.yaml"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		logFile, err := os.Create(filepath.Join(home, "mihomo.log"))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(mihomo, "-d", home, "-f", filepath.Join(home, "config.yaml"))
		cmd.Stdout, cmd.Stderr = logFile, logFile
		if err := cmd.Start(); err != nil {
			t.Fatalf("start mihomo: %v", err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			if t.Failed() {
				out, _ := os.ReadFile(logFile.Name())
				t.Logf("mihomo log:\n%s\nprofile:\n%s", out, profile)
			}
			_ = logFile.Close()
		})
		waitUntil(t, 20*time.Second, "a page through mihomo", func() error {
			return xhttpE2EFetch(fmt.Sprintf("127.0.0.1:%d", port), page)
		})
	})
}

func xhttpE2EFetch(socks, page string) error {
	got, err := socksGet(socks, page)
	if err == nil && got != "xhttp-ok" {
		err = fmt.Errorf("body %q", got)
	}
	return err
}
