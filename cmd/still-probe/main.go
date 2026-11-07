// Command still-probe checks a still-ui panel's inbounds from inside a censored
// network: it fetches a job from the panel, dials every inbound through a local
// Xray as a real client would, downloads a test URL and reports the results.
// It uses the standard library only, so it builds as a static binary.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var version = "dev"

const (
	defaultConfigPath = "/etc/still-probe/config.json"
	checkTimeout      = 30 * time.Second
	panelTimeout      = 20 * time.Second
	maxDownload       = 64 << 20
	maxJobBytes       = 1 << 20
	checkConcurrency  = 4
	// The timer fires every minute; a run inside the job's interval exits at once.
	intervalSlack = 15 * time.Second
)

type config struct {
	Panel string `json:"panel"`
	Token string `json:"token"`
	Xray  string `json:"xray"`
	State string `json:"stateDir"`
	// PanelCertSHA256 pins the panel's leaf certificate (hex SHA-256) for a
	// panel behind a self-signed certificate; empty means normal verification.
	PanelCertSHA256 string `json:"panelCertSha256"`
	// InsecureHTTP lets an http:// panel through: the token and every job's keys cross the
	// network in clear text, readable by the censor the probe sits behind.
	InsecureHTTP bool `json:"insecureHttp,omitempty"`
}

type jobTarget struct {
	Key       string          `json:"key"`
	Label     string          `json:"label"`
	InboundID int             `json:"inboundId"`
	Protocol  string          `json:"protocol"`
	Outbound  json.RawMessage `json:"outbound"`
}

type job struct {
	Probe       string      `json:"probe"`
	IntervalSec int         `json:"intervalSec"`
	TestURL     string      `json:"testUrl"`
	DirectURL   string      `json:"directUrl,omitempty"`
	Targets     []jobTarget `json:"targets"`
}

type result struct {
	Key     string `json:"key"`
	Ok      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	TtfbMs  int    `json:"ttfbMs"`
	TotalMs int    `json:"totalMs"`
	Bytes   int64  `json:"bytes"`
}

type report struct {
	AgentVersion string   `json:"agentVersion"`
	Results      []result `json:"results"`
}

type state struct {
	LastRun int64 `json:"lastRun"`
	Job     *job  `json:"job,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(version)
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		cfgPath := fs.String("config", defaultConfigPath, "config file")
		force := fs.Bool("force", false, "run now even inside the interval")
		_ = fs.Parse(os.Args[2:])
		cfg, err := loadConfig(*cfgPath)
		if err != nil {
			fatal(err)
		}
		if err := run(cfg, *force, time.Now); err != nil {
			fatal(err)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: still-probe run [-config "+defaultConfigPath+"] [-force] | still-probe version")
	os.Exit(2)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "still-probe:", err)
	os.Exit(1)
}

func loadConfig(path string) (*config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &config{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if !strings.HasSuffix(cfg.Panel, "/") {
		cfg.Panel += "/"
	}
	u, err := url.Parse(cfg.Panel)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%s: panel must be the panel URL with its base path, e.g. https://panel.example.com:2053/path/", path)
	}
	if u.Scheme == "http" {
		if !cfg.InsecureHTTP {
			return nil, fmt.Errorf("%s: the panel URL is http://, which would send the probe token and the inbound keys in clear text: use the panel's https:// address (with panelCertSha256 for a self-signed certificate), or set \"insecureHttp\": true to accept that", path)
		}
		fmt.Fprintln(os.Stderr, "still-probe: WARNING: talking to the panel over plain http://; the token and the inbound keys cross the network unencrypted")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("%s: token is empty", path)
	}
	if cfg.Xray == "" {
		cfg.Xray = "/usr/local/still-probe/xray"
	}
	if cfg.State == "" {
		cfg.State = "/var/lib/still-probe"
	}
	return cfg, nil
}

func (c *config) endpoint(p string) string { return c.Panel + "probe/v1/" + p }

// panelClient talks to the panel directly, or through a SOCKS port of the
// local Xray when the panel itself is blocked on the direct path.
func (c *config) panelClient(socksPort int) (*http.Client, error) {
	tr := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second}
	if socksPort > 0 {
		tr.Proxy = http.ProxyURL(&url.URL{Scheme: "socks5", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))})
	}
	if c.PanelCertSHA256 != "" {
		want, err := hex.DecodeString(strings.ReplaceAll(strings.ToLower(c.PanelCertSHA256), ":", ""))
		if err != nil || len(want) != sha256.Size {
			return nil, errors.New("panelCertSha256 must be 64 hex characters")
		}
		tr.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, // replaced by the pin check below
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("panel sent no certificate")
				}
				sum := sha256.Sum256(raw[0])
				if !strings.EqualFold(hex.EncodeToString(sum[:]), hex.EncodeToString(want)) {
					return errors.New("panel certificate does not match panelCertSha256")
				}
				return nil
			},
		}
	}
	return &http.Client{Transport: tr, Timeout: panelTimeout}, nil
}

func (c *config) statePath() string { return filepath.Join(c.State, "state.json") }

func (c *config) loadState() state {
	var st state
	if raw, err := os.ReadFile(c.statePath()); err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func (c *config) saveState(st state) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.State, 0o700); err != nil {
		return err
	}
	tmp := c.statePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.statePath())
}

func dueToRun(st state, now time.Time) bool {
	if st.LastRun == 0 || st.Job == nil || st.Job.IntervalSec <= 0 {
		return true
	}
	next := time.Unix(st.LastRun, 0).Add(time.Duration(st.Job.IntervalSec)*time.Second - intervalSlack)
	return !now.Before(next)
}

func (c *config) fetchJob() (*job, error) {
	client, err := c.panelClient(0)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, c.endpoint("job"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("User-Agent", "still-probe/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJobBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("panel answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	j := &job{}
	if err := json.Unmarshal(body, j); err != nil {
		return nil, fmt.Errorf("job: %w", err)
	}
	return j, nil
}

func run(cfg *config, force bool, now func() time.Time) error {
	st := cfg.loadState()
	if !force && !dueToRun(st, now()) {
		return nil
	}
	j, err := cfg.fetchJob()
	if err != nil {
		if st.Job == nil {
			return fmt.Errorf("fetch job: %w", err)
		}
		fmt.Println("job fetch failed, using the last job:", err)
		j = st.Job
	}
	st.Job, st.LastRun = j, now().Unix()
	if err := cfg.saveState(st); err != nil {
		return err
	}

	var xr *xrayRun
	if len(j.Targets) > 0 {
		xr, err = startXray(cfg.Xray, cfg.State, j.Targets)
		if err != nil {
			return err
		}
		defer xr.stop()
	}
	results := checkAll(j, xr)
	if len(j.Targets) > 0 && !xr.ready {
		fmt.Println("local xray did not start: the targets are not reported this run")
		if len(results) == 0 {
			return errors.New("local xray did not start and the job has no direct check")
		}
	}
	for _, r := range results {
		status := "ok  "
		if !r.Ok {
			status = "FAIL"
		}
		fmt.Printf("%s %s ttfb=%dms total=%dms bytes=%d %s\n", status, r.Key, r.TtfbMs, r.TotalMs, r.Bytes, r.Error)
	}
	return cfg.sendReport(results, xr)
}

// checkAll leaves the targets out when the local Xray is not up: the panel would
// read this agent's own failure as every inbound blocked.
func checkAll(j *job, xr *xrayRun) []result {
	var results []result
	if xr != nil && xr.ready {
		results = make([]result, len(j.Targets))
		sem := make(chan struct{}, checkConcurrency)
		var wg sync.WaitGroup
		for i, t := range j.Targets {
			wg.Add(1)
			go func(i int, t jobTarget) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				results[i] = fetch(t.Key, j.TestURL, xr.ports[i])
			}(i, t)
		}
		wg.Wait()
	}
	if j.DirectURL != "" {
		results = append(results, fetch("direct", j.DirectURL, 0))
	}
	return results
}

// fetch downloads target fully, through SOCKS port socksPort when it is set.
// A censor that lets the handshake through and freezes the stream after a few
// kilobytes shows up here as a read error, not as success.
func fetch(key, target string, socksPort int) result {
	r := result{Key: key}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 15 * time.Second}
	if socksPort > 0 {
		tr.Proxy = http.ProxyURL(&url.URL{Scheme: "socks5", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))})
	}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	start := time.Now()
	var firstByte time.Time
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { firstByte = time.Now() }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, target, nil)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		r.TotalMs = int(time.Since(start).Milliseconds())
		r.Error = shortError(err)
		return r
	}
	defer resp.Body.Close()
	if !firstByte.IsZero() {
		r.TtfbMs = int(firstByte.Sub(start).Milliseconds())
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxDownload))
	r.Bytes = n
	r.TotalMs = int(time.Since(start).Milliseconds())
	switch {
	case err != nil:
		r.Error = fmt.Sprintf("stalled after %d bytes: %s", n, shortError(err))
	case resp.StatusCode < 200 || resp.StatusCode >= 400:
		r.Error = "HTTP " + strconv.Itoa(resp.StatusCode)
	case resp.ContentLength > 0 && n < resp.ContentLength:
		r.Error = fmt.Sprintf("got %d of %d bytes", n, resp.ContentLength)
	default:
		r.Ok = true
	}
	return r
}

func shortError(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "connection reset"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection refused"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "connection closed"
	}
	msg := err.Error()
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return msg
}

// sendReport posts directly first; when the panel is unreachable from here,
// it retries through each inbound that just worked.
func (c *config) sendReport(results []result, xr *xrayRun) error {
	body, err := json.Marshal(report{AgentVersion: version, Results: results})
	if err != nil {
		return err
	}
	ports := []int{0}
	if xr != nil && xr.ready {
		for i, r := range results {
			if r.Ok && i < len(xr.ports) {
				ports = append(ports, xr.ports[i])
			}
		}
	}
	var lastErr error
	for _, port := range ports {
		if lastErr = c.postReport(body, port); lastErr == nil {
			if port > 0 {
				fmt.Println("report sent through the VPN: the panel is not reachable directly")
			}
			return nil
		}
	}
	return fmt.Errorf("send report: %w", lastErr)
}

func (c *config) postReport(body []byte, socksPort int) error {
	client, err := c.panelClient(socksPort)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.endpoint("report"), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "still-probe/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("panel answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
