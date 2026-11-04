package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A censor that freezes a connection after its first kilobytes is the case
// the probe exists for: a partial download must be a failure, not a success.
func TestFetchCountsAStreamCutMidwayAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "262144")
		_, _ = w.Write(make([]byte, 16384))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer srv.Close()
	r := fetch("k", srv.URL, 0)
	if r.Ok || r.Bytes != 16384 || !strings.HasPrefix(r.Error, "stalled after 16384 bytes") {
		t.Fatalf("result %+v", r)
	}
}

func TestFetchMeasuresAFullDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write(make([]byte, 100000))
	}))
	defer srv.Close()
	r := fetch("k", srv.URL, 0)
	if !r.Ok || r.Bytes != 100000 || r.TtfbMs < 20 || r.TotalMs < r.TtfbMs || r.Error != "" {
		t.Fatalf("result %+v", r)
	}
}

func TestFetchTreatsAnErrorStatusAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusForbidden)
	}))
	defer srv.Close()
	if r := fetch("k", srv.URL, 0); r.Ok || r.Error != "HTTP 403" {
		t.Fatalf("result %+v", r)
	}
}

func TestBuildXrayConfigRoutesEachSocksPortToItsOwnOutbound(t *testing.T) {
	targets := []jobTarget{
		{Key: "1@a:443", Outbound: json.RawMessage(`{"protocol":"vless","tag":"probe","settings":{"address":"a","port":443}}`)},
		{Key: "2@b:443", Outbound: json.RawMessage(`{"protocol":"trojan","tag":"probe","settings":{"address":"b","port":443}}`)},
	}
	raw, err := buildXrayConfig(targets, []int{20001, 20002})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Inbounds []struct {
			Tag    string `json:"tag"`
			Listen string `json:"listen"`
			Port   int    `json:"port"`
		} `json:"inbounds"`
		Outbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
		} `json:"outbounds"`
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inbounds) != 2 || cfg.Inbounds[1].Port != 20002 || cfg.Inbounds[0].Listen != "127.0.0.1" {
		t.Fatalf("inbounds %+v", cfg.Inbounds)
	}
	if cfg.Outbounds[0].Tag != "out-0" || cfg.Outbounds[1].Protocol != "trojan" || cfg.Outbounds[1].Tag != "out-1" ||
		cfg.Outbounds[2].Protocol != "blackhole" {
		t.Fatalf("outbounds %+v", cfg.Outbounds)
	}
	if r := cfg.Routing.Rules[1]; r.InboundTag[0] != "in-1" || r.OutboundTag != "out-1" {
		t.Fatalf("rules %+v", cfg.Routing.Rules)
	}
}

func TestDueToRunFollowsTheJobInterval(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st := state{LastRun: now.Add(-9 * time.Minute).Unix(), Job: &job{IntervalSec: 600}}
	if dueToRun(st, now) {
		t.Fatal("ran 9 minutes into a 10-minute interval")
	}
	if !dueToRun(st, now.Add(46*time.Second)) {
		t.Fatal("did not run once the interval (less the timer slack) passed")
	}
	if !dueToRun(state{}, now) {
		t.Fatal("a first run must not wait")
	}
}

type fakePanel struct {
	mu      sync.Mutex
	jobDown bool
	job     job
	reports []report
	auth    []string
}

func (p *fakePanel) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/base/probe/v1/job", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.auth = append(p.auth, r.Header.Get("Authorization"))
		if p.jobDown {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(p.job)
	})
	mux.HandleFunc("/base/probe/v1/report", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		var rep report
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
			t.Errorf("report body: %v", err)
		}
		p.reports = append(p.reports, rep)
		_, _ = io.WriteString(w, `{"stored":1}`)
	})
	return mux
}

func TestRunReportsDirectCheckAndFallsBackToTheCachedJob(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer site.Close()
	p := &fakePanel{job: job{Probe: "home-1", IntervalSec: 600, DirectURL: site.URL}}
	panelSrv := httptest.NewServer(p.handler(t))
	defer panelSrv.Close()
	cfg := &config{Panel: panelSrv.URL + "/base/", Token: "tok", State: t.TempDir(), Xray: "/nonexistent"}

	clock := time.Unix(1_800_000_000, 0)
	if err := run(cfg, false, func() time.Time { return clock }); err != nil {
		t.Fatal(err)
	}
	if len(p.reports) != 1 || len(p.reports[0].Results) != 1 || p.reports[0].Results[0].Key != "direct" || !p.reports[0].Results[0].Ok {
		t.Fatalf("reports %+v", p.reports)
	}
	if p.auth[0] != "Bearer tok" {
		t.Fatalf("authorization %q", p.auth[0])
	}
	if err := run(cfg, false, func() time.Time { return clock.Add(time.Minute) }); err != nil || len(p.reports) != 1 {
		t.Fatalf("ran again inside the interval: %v, %d reports", err, len(p.reports))
	}

	p.jobDown = true
	if err := run(cfg, true, func() time.Time { return clock.Add(2 * time.Minute) }); err != nil {
		t.Fatal(err)
	}
	if len(p.reports) != 2 || p.reports[1].Results[0].Key != "direct" {
		t.Fatalf("the cached job was not used while the panel job route failed: %+v", p.reports)
	}
}

func TestPanelCertificatePin(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	for _, tc := range []struct {
		pin  string
		ok   bool
		name string
	}{
		{hex.EncodeToString(sum[:]), true, "matching pin"},
		{strings.Repeat("ab", 32), false, "other pin"},
		{"", false, "no pin, self-signed"},
	} {
		cfg := &config{PanelCertSHA256: tc.pin}
		client, err := cfg.panelClient(0)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		if (err == nil) != tc.ok {
			t.Errorf("%s: err %v", tc.name, err)
		}
	}
	if _, err := (&config{PanelCertSHA256: "xyz"}).panelClient(0); err == nil {
		t.Fatal("a malformed pin was accepted")
	}
}
