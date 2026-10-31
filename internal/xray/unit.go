package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

// ErrConfigRejected reports a config that `xray run -test` refused; the running core was not touched.
var ErrConfigRejected = errors.New("xray rejected the new config")

// RollbackError reports a config that passed the test but did not start; the unit runs the last good one again.
type RollbackError struct{ Cause error }

func (e *RollbackError) Error() string {
	return fmt.Sprintf("new xray config did not start, the previous one is running again: %v", e.Cause)
}

func (e *RollbackError) Unwrap() error { return e.Cause }

var (
	unitStartTimeout  = 15 * time.Second
	unitSettleTime    = 1500 * time.Millisecond
	unitStateCacheTTL = time.Second
	unitTestTimeout   = 90 * time.Second

	// systemctlCommand and journalCommand are swapped by tests for fakes of the real tools.
	systemctlCommand = func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	}
	journalCommand = func(ctx context.Context, unit string, lines int) ([]byte, error) {
		return exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", strconv.Itoa(lines), "--no-pager", "-o", "cat").Output()
	}
	testConfigCommand = func(ctx context.Context, configPath string) ([]byte, error) {
		return exec.CommandContext(ctx, GetBinaryPath(), "run", "-test", "-c", configPath).CombinedOutput()
	}
)

// UnitMode reports whether Xray runs as its own systemd unit rather than as the panel's child.
func UnitMode() bool { return config.GetXrayUnit() != "" }

func unitFile(name string) string { return filepath.Join(config.GetXrayUnitFolder(), name) }

func unitConfigPath() string    { return unitFile("config.json") }
func unitLastGoodPath() string  { return unitFile("config.last-good.json") }
func unitCandidatePath() string { return unitFile("config.candidate.json") }
func unitMetaPath() string      { return unitFile("panel-meta.json") }

type unitState struct {
	Active    string
	MainPID   int
	NRestarts int
	StartMono uint64
}

func (s unitState) running() bool { return s.Active == "active" || s.Active == "reloading" }

// unitControl drives the systemd unit the core runs in.
type unitControl struct {
	name string

	mu        sync.Mutex
	cached    unitState
	cachedAt  time.Time
	restarts  int // NRestarts already accounted for, so only systemd's own restarts count as crashes
	baselined bool
	journal   string
	journalAt time.Time
}

func newUnitControl(name string) *unitControl { return &unitControl{name: name} }

func (u *unitControl) systemctl(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return systemctlCommand(ctx, args...)
}

// state returns the unit's state, cached for a second unless fresh is set; a rise in
// NRestarts that this panel did not cause is reported through OnCrash.
func (u *unitControl) state(fresh bool) unitState {
	u.mu.Lock()
	if !fresh && time.Since(u.cachedAt) < unitStateCacheTTL {
		st := u.cached
		u.mu.Unlock()
		return st
	}
	u.mu.Unlock()

	out, err := u.systemctl(5*time.Second, "show", u.name,
		"-p", "ActiveState", "-p", "MainPID", "-p", "NRestarts", "-p", "ExecMainStartTimestampMonotonic")
	st := unitState{Active: "unknown"}
	if err == nil {
		st = parseUnitShow(string(out))
	}

	u.mu.Lock()
	u.cached, u.cachedAt = st, time.Now()
	crashed := u.baselined && st.NRestarts > u.restarts
	if err == nil {
		u.restarts, u.baselined = st.NRestarts, true
	}
	u.mu.Unlock()
	if crashed && OnCrash != nil {
		OnCrash(fmt.Errorf("%s was restarted by systemd after a crash (NRestarts=%d)", u.name, st.NRestarts))
	}
	return st
}

func parseUnitShow(out string) unitState {
	st := unitState{}
	for line := range strings.SplitSeq(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			st.Active = value
		case "MainPID":
			st.MainPID, _ = strconv.Atoi(value)
		case "NRestarts":
			st.NRestarts, _ = strconv.Atoi(value)
		case "ExecMainStartTimestampMonotonic":
			st.StartMono, _ = strconv.ParseUint(value, 10, 64)
		}
	}
	return st
}

// journalTail returns the last lines the core wrote, cached for 10 s: the status page polls every 2 s.
func (u *unitControl) journalTail() string {
	u.mu.Lock()
	if time.Since(u.journalAt) < 10*time.Second {
		j := u.journal
		u.mu.Unlock()
		return j
	}
	u.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := journalCommand(ctx, u.name, 5)
	tail := strings.TrimSpace(string(out))
	u.mu.Lock()
	u.journal, u.journalAt = tail, time.Now()
	u.mu.Unlock()
	return tail
}

// restartAndWait restarts the unit and waits until it is active, its API port answers and it
// stays up for unitSettleTime; anything else is an error carrying the unit's last log lines.
func (u *unitControl) restartAndWait(apiPort int) error {
	_, _ = u.systemctl(10*time.Second, "reset-failed", u.name)
	if out, err := u.systemctl(unitStartTimeout+5*time.Second, "restart", u.name); err != nil {
		return fmt.Errorf("systemctl restart %s: %v: %s", u.name, err, strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(unitStartTimeout)
	var st unitState
	for {
		st = u.state(true)
		if st.running() && apiPortAnswers(apiPort) {
			break
		}
		if st.Active == "failed" || time.Now().After(deadline) {
			u.forgetJournal()
			return fmt.Errorf("%s did not start (%s): %s", u.name, st.Active, u.journalTail())
		}
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(unitSettleTime)
	settled := u.state(true)
	if !settled.running() || settled.MainPID != st.MainPID {
		u.forgetJournal()
		return fmt.Errorf("%s exited right after start: %s", u.name, u.journalTail())
	}
	u.forgetJournal()
	return nil
}

func (u *unitControl) forgetJournal() {
	u.mu.Lock()
	u.journalAt = time.Time{}
	u.mu.Unlock()
}

func (u *unitControl) stop() error {
	if out, err := u.systemctl(30*time.Second, "stop", u.name); err != nil {
		return fmt.Errorf("systemctl stop %s: %v: %s", u.name, err, strings.TrimSpace(string(out)))
	}
	u.state(true)
	return nil
}

// uptime is how long the unit's current core has been running, from systemd's monotonic start stamp.
func (u *unitControl) uptime() uint64 {
	st := u.state(false)
	if !st.running() || st.StartMono == 0 {
		return 0
	}
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0
	}
	now, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || now*1e6 < float64(st.StartMono) {
		return 0
	}
	return uint64(now - float64(st.StartMono)/1e6)
}

func apiPortAnswers(port int) bool {
	if port <= 0 {
		return true
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// unitMeta keeps what the config file cannot carry (json:"-" fields), so a restarted
// panel can take over the running core with the exact config it was given.
type unitMeta struct {
	TuicRelays map[string]int `json:"tuicRelays,omitempty"`
}

func writeUnitMeta(cfg *Config) error {
	meta := unitMeta{}
	for _, ib := range cfg.InboundConfigs {
		if ib.TuicRelay {
			if meta.TuicRelays == nil {
				meta.TuicRelays = map[string]int{}
			}
			meta.TuicRelays[ib.Tag] = ib.TuicInboundID
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return writeFileAtomic(unitMetaPath(), data, 0o600)
}

func applyUnitMeta(cfg *Config) {
	data, err := os.ReadFile(unitMetaPath())
	if err != nil {
		return
	}
	var meta unitMeta
	if json.Unmarshal(data, &meta) != nil {
		return
	}
	for i := range cfg.InboundConfigs {
		if id, ok := meta.TuicRelays[cfg.InboundConfigs[i].Tag]; ok {
			cfg.InboundConfigs[i].TuicRelay = true
			cfg.InboundConfigs[i].TuicInboundID = id
		}
	}
}

// writeUnitConfig installs data as the unit's config and as its last known good copy.
func writeUnitConfig(cfg *Config, data []byte) error {
	if err := writeFileAtomic(unitConfigPath(), data, 0o640); err != nil {
		return err
	}
	if err := writeFileAtomic(unitLastGoodPath(), data, 0o640); err != nil {
		return err
	}
	return writeUnitMeta(cfg)
}

// startUnit tests the config, installs it and restarts the unit; a config that passes the test
// but does not start is replaced by the last good one, which is started again.
func (p *process) startUnit() error {
	data, err := json.MarshalIndent(p.config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to generate xray config: %w", err)
	}
	if err := os.MkdirAll(config.GetXrayUnitFolder(), 0o750); err != nil {
		return err
	}
	candidate := unitCandidatePath()
	if err := writeFileAtomic(candidate, data, 0o640); err != nil {
		return fmt.Errorf("failed to write xray config: %w", err)
	}
	defer os.Remove(candidate)

	ctx, cancel := context.WithTimeout(context.Background(), unitTestTimeout)
	out, testErr := testConfigCommand(ctx, candidate)
	cancel()
	if testErr != nil {
		return fmt.Errorf("%w: %s", ErrConfigRejected, lastLines(string(out), 3))
	}

	previous, prevErr := os.ReadFile(unitLastGoodPath())
	if err := writeFileAtomic(unitConfigPath(), data, 0o640); err != nil {
		return fmt.Errorf("failed to write xray config: %w", err)
	}
	cause := p.unit.restartAndWait(apiPortOf(p.config))
	if cause == nil {
		if err := writeFileAtomic(unitLastGoodPath(), data, 0o640); err != nil {
			logger.Warning("xray unit: could not record the last good config:", err)
		}
		if err := writeUnitMeta(p.config); err != nil {
			logger.Warning("xray unit: could not record config metadata:", err)
		}
		return nil
	}
	if prevErr != nil || string(previous) == string(data) {
		return cause
	}
	logger.Error("xray unit: new config did not start, restoring the last good one:", cause)
	if err := writeFileAtomic(unitConfigPath(), previous, 0o640); err != nil {
		return errors.Join(cause, fmt.Errorf("rollback: %w", err))
	}
	var prevCfg Config
	_ = json.Unmarshal(previous, &prevCfg)
	if err := p.unit.restartAndWait(apiPortOf(&prevCfg)); err != nil {
		return errors.Join(cause, fmt.Errorf("rollback: %w", err))
	}
	return &RollbackError{Cause: cause}
}

func apiPortOf(cfg *Config) int {
	if cfg == nil {
		return 0
	}
	for _, inbound := range cfg.InboundConfigs {
		if inbound.Tag == "api" {
			return inbound.Port
		}
	}
	return 0
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// clearNullSections undoes RawMessage's nil -> "null" marshalling, so a config read back from
// disk compares equal to the generated one it was written from.
func clearNullSections(v reflect.Value) {
	rawType := reflect.TypeFor[json_util.RawMessage]()
	for i := range v.NumField() {
		field := v.Field(i)
		if field.Type() == rawType && string(field.Bytes()) == "null" {
			field.SetBytes(nil)
		}
	}
}

// AdoptRunningUnit returns the core already running in the unit, with the config it was
// started from, so a restarted panel takes it over instead of restarting it; nil otherwise.
func AdoptRunningUnit() *Process {
	name := config.GetXrayUnit()
	if name == "" {
		return nil
	}
	u := newUnitControl(name)
	if !u.state(true).running() {
		return nil
	}
	data, err := os.ReadFile(unitConfigPath())
	if err != nil {
		logger.Warning("xray unit: running, but its config cannot be read:", err)
		return nil
	}
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		logger.Warning("xray unit: running, but its config cannot be parsed:", err)
		return nil
	}
	clearNullSections(reflect.ValueOf(cfg).Elem())
	for i := range cfg.InboundConfigs {
		clearNullSections(reflect.ValueOf(&cfg.InboundConfigs[i]).Elem())
	}
	applyUnitMeta(cfg)
	p := &Process{newProcess(cfg)}
	p.unit = u
	p.refreshVersion()
	p.refreshAPIPort()
	return p
}

// Recovering reports a unit that systemd itself is restarting after a crash: the panel must
// not start a second restart of its own on top of it.
func (p *Process) Recovering() bool {
	if p.unit == nil {
		return false
	}
	switch p.unit.state(false).Active {
	case "activating", "deactivating":
		return true
	}
	return false
}
