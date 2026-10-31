package xray

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

// fakeUnit stands in for systemd: "restart" starts the core from the installed config.json,
// which fails when the config holds "will-not-start" (it passes `xray run -test`).
type fakeUnit struct {
	mu        sync.Mutex
	active    string
	pid       int
	nrestarts int
	calls     []string
}

func (f *fakeUnit) systemctl(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args[0])
	switch args[0] {
	case "show":
		return fmt.Appendf(nil, "ActiveState=%s\nMainPID=%d\nNRestarts=%d\nExecMainStartTimestampMonotonic=1\n",
			f.active, f.pid, f.nrestarts), nil
	case "restart":
		data, err := os.ReadFile(unitConfigPath())
		if err != nil || strings.Contains(string(data), "will-not-start") {
			f.active, f.pid = "failed", 0
			return nil, nil
		}
		f.active, f.pid = "active", f.pid+100
	case "stop":
		f.active, f.pid = "inactive", 0
	}
	return nil, nil
}

func (f *fakeUnit) count(verb string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == verb {
			n++
		}
	}
	return n
}

func setupFakeUnit(t *testing.T) *fakeUnit {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("systemd units are Linux only")
	}
	t.Setenv("XUI_XRAY_UNIT", "still-xray-test")
	t.Setenv("XUI_XRAY_UNIT_FOLDER", t.TempDir())
	f := &fakeUnit{active: "active", pid: 7}
	prevSystemctl, prevJournal, prevTest := systemctlCommand, journalCommand, testConfigCommand
	prevStart, prevSettle := unitStartTimeout, unitSettleTime
	systemctlCommand = f.systemctl
	journalCommand = func(context.Context, string, int) ([]byte, error) { return []byte("core: boom"), nil }
	testConfigCommand = func(_ context.Context, path string) ([]byte, error) {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "rejected-by-test") {
			return []byte("Failed to start: invalid config"), errors.New("exit status 23")
		}
		return []byte("Configuration OK."), nil
	}
	unitStartTimeout, unitSettleTime = time.Second, 10*time.Millisecond
	t.Cleanup(func() {
		systemctlCommand, journalCommand, testConfigCommand = prevSystemctl, prevJournal, prevTest
		unitStartTimeout, unitSettleTime = prevStart, prevSettle
	})
	return f
}

func unitTestConfig(logLevel string) *Config {
	return &Config{
		LogConfig: json_util.RawMessage(fmt.Sprintf(`{"loglevel":%q}`, logLevel)),
		InboundConfigs: []InboundConfig{{
			Listen: json_util.RawMessage(`"127.0.0.1"`), Port: 64001, Protocol: "socks", Tag: "tuic-relay-1",
			TuicRelay: true, TuicInboundID: 1, Settings: json_util.RawMessage(`{"auth":"password","accounts":[{"user":"a","pass":"p"}]}`),
		}},
	}
}

func startGood(t *testing.T) {
	t.Helper()
	if err := NewProcess(unitTestConfig("warning")).Start(); err != nil {
		t.Fatalf("first start: %v", err)
	}
}

func TestUnitStartRejectedConfigLeavesTheRunningCoreAlone(t *testing.T) {
	f := setupFakeUnit(t)
	startGood(t)
	before, _ := os.ReadFile(unitConfigPath())

	err := NewProcess(unitTestConfig("rejected-by-test")).Start()

	if !errors.Is(err, ErrConfigRejected) || !strings.Contains(err.Error(), "invalid config") {
		t.Fatalf("Start() = %v, want ErrConfigRejected with the test output", err)
	}
	if got := f.count("restart"); got != 1 {
		t.Fatalf("restarts = %d, want only the first start's", got)
	}
	after, _ := os.ReadFile(unitConfigPath())
	if string(after) != string(before) {
		t.Fatal("a rejected config replaced the installed one")
	}
}

func TestUnitStartRollsBackAConfigThatDoesNotStart(t *testing.T) {
	f := setupFakeUnit(t)
	startGood(t)
	good, _ := os.ReadFile(unitConfigPath())

	err := NewProcess(unitTestConfig("will-not-start")).Start()

	var rollback *RollbackError
	if !errors.As(err, &rollback) || !strings.Contains(err.Error(), "core: boom") {
		t.Fatalf("Start() = %v, want a RollbackError carrying the unit's log", err)
	}
	installed, _ := os.ReadFile(unitConfigPath())
	if string(installed) != string(good) {
		t.Fatal("the unit was not given its last good config back")
	}
	if f.active != "active" || f.count("restart") != 3 {
		t.Fatalf("unit %s after %d restarts, want active after start, failed start, rollback", f.active, f.count("restart"))
	}
}

func TestUnitStartInstallsAConfigTheCoreCanRead(t *testing.T) {
	setupFakeUnit(t)
	startGood(t)

	info, err := os.Stat(unitConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("config mode %o, want 0640 (group of the unit's user)", info.Mode().Perm())
	}
	lastGood, _ := os.ReadFile(unitLastGoodPath())
	installed, _ := os.ReadFile(unitConfigPath())
	if string(lastGood) != string(installed) {
		t.Fatal("a config that started was not recorded as the last good one")
	}
}

func TestAdoptRunningUnitTakesOverWithoutAnyChange(t *testing.T) {
	f := setupFakeUnit(t)
	original := unitTestConfig("warning")
	if err := NewProcess(original).Start(); err != nil {
		t.Fatal(err)
	}

	adopted := AdoptRunningUnit()

	if adopted == nil {
		t.Fatal("a running unit was not adopted")
	}
	diff, ok := ComputeHotDiff(adopted.GetConfig(), original)
	if !ok || !diff.Empty() {
		t.Fatalf("adopted config differs from the one started (ok=%v diff=%+v)", ok, diff)
	}
	if f.count("restart") != 1 {
		t.Fatal("adoption restarted the core")
	}
	f.active = "inactive"
	if AdoptRunningUnit() != nil {
		t.Fatal("a stopped unit was adopted")
	}
}

func TestDroppingAUnitProcessNeverStopsTheUnit(t *testing.T) {
	f := setupFakeUnit(t)
	startGood(t)
	for range 3 {
		_ = NewProcess(unitTestConfig("warning"))
	}
	for range 5 {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
	if f.count("stop") != 0 {
		t.Fatal("a garbage-collected Process stopped the shared unit")
	}
}

func TestUnitCrashIsReportedOncePerSystemdRestart(t *testing.T) {
	f := setupFakeUnit(t)
	var crashes []string
	prev := OnCrash
	OnCrash = func(err error) { crashes = append(crashes, err.Error()) }
	t.Cleanup(func() { OnCrash = prev })

	u := newUnitControl("still-xray-test")
	u.state(true)
	f.nrestarts = 1
	u.state(true)
	u.state(true)

	if len(crashes) != 1 || !strings.Contains(crashes[0], "NRestarts=1") {
		t.Fatalf("crashes = %q, want one report of NRestarts=1", crashes)
	}
	if !slices.Contains(f.calls, "show") {
		t.Fatal("state was not read from systemd")
	}
}
