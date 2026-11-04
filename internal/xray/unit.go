package xray

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// unitRestarted is set when systemd restarted the core by itself: that core started from the
// config on disk, which may miss clients added through the API since the last full apply.
var unitRestarted atomic.Bool

// unitStarts counts start boundaries of panel-requested restarts: odd while one is under way.
var unitStarts atomic.Uint64

// TakeUnitRestart reports, once, that systemd restarted the core since the last call.
func TakeUnitRestart() bool { return unitRestarted.Swap(false) }

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
	baseGen   uint64 // unitStarts when restarts was taken
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
	// Restarts during or since a start this panel requested are that start's, not crashes.
	gen := unitStarts.Load()
	crashed := u.baselined && gen%2 == 0 && gen == u.baseGen && st.NRestarts > u.restarts
	if err == nil {
		u.restarts, u.baselined, u.baseGen = st.NRestarts, true, gen
	}
	u.mu.Unlock()
	if crashed {
		unitRestarted.Store(true)
	}
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
	out, _ := journalCommand(ctx, u.name, 30)
	tail := failureLines(string(out))
	u.mu.Lock()
	u.journal, u.journalAt = tail, time.Now()
	u.mu.Unlock()
	return tail
}

// restartAndWait restarts the unit and waits until it is active, its API port answers and it
// stays up for unitSettleTime; anything else is an error carrying the unit's last log lines.
func (u *unitControl) restartAndWait(apiPort int) error {
	unitStarts.Add(1)
	defer unitStarts.Add(1)
	_, _ = u.systemctl(10*time.Second, "reset-failed", u.name)
	if out, err := u.systemctl(unitStartTimeout+5*time.Second, "restart", u.name); err != nil {
		return fmt.Errorf("systemctl restart %s: %w: %s", u.name, err, strings.TrimSpace(string(out)))
	}
	started := u.state(true)
	deadline := time.Now().Add(unitStartTimeout)
	var st unitState
	for {
		st = u.state(true)
		if st.running() && apiPortAnswers(apiPort) {
			break
		}
		// A core systemd already had to restart once will not come up with this config.
		if st.Active == "failed" || st.NRestarts > started.NRestarts || time.Now().After(deadline) {
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

// failureLines keeps what explains a failed start: the core's error lines, not its routine warnings.
func failureLines(journal string) string {
	var picked []string
	for line := range strings.SplitSeq(strings.TrimSpace(journal), "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "failed") || strings.Contains(lower, "error") {
			picked = append(picked, strings.TrimSpace(line))
		}
	}
	if len(picked) == 0 {
		return lastLines(journal, 1)
	}
	return lastLines(strings.Join(picked, "\n"), 2)
}

func (u *unitControl) forgetJournal() {
	u.mu.Lock()
	u.journalAt = time.Time{}
	u.mu.Unlock()
}

func (u *unitControl) stop() error {
	if out, err := u.systemctl(30*time.Second, "stop", u.name); err != nil {
		return fmt.Errorf("systemctl stop %s: %w: %s", u.name, err, strings.TrimSpace(string(out)))
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
	conn, err := (&net.Dialer{Timeout: 300 * time.Millisecond}).DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// unitMeta keeps what the config file cannot carry (json:"-" fields), so a restarted
// panel can take over the running core with the exact config it was given.
type unitMeta struct {
	TuicRelays     map[string]int    `json:"tuicRelays,omitempty"`
	RelayPasswords map[string]string `json:"relayPasswords,omitempty"`
	CoreVersion    string            `json:"coreVersion,omitempty"`
}

// RelayPasswords supplies the loopback relay passwords kept beside the unit's config (0600, root),
// so a restarted panel reuses them instead of trusting whatever a config's inbounds claim.
var RelayPasswords func() map[string]string

func writeUnitMeta(cfg *Config, coreVersion string) error {
	meta := unitMeta{CoreVersion: coreVersion}
	if RelayPasswords != nil {
		meta.RelayPasswords = RelayPasswords()
	}
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

func readUnitMeta() unitMeta {
	var meta unitMeta
	if data, err := os.ReadFile(unitMetaPath()); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	return meta
}

func applyUnitMeta(cfg *Config, meta unitMeta) {
	for i := range cfg.InboundConfigs {
		if id, ok := meta.TuicRelays[cfg.InboundConfigs[i].Tag]; ok {
			cfg.InboundConfigs[i].TuicRelay = true
			cfg.InboundConfigs[i].TuicInboundID = id
		}
	}
}

// writeUnitConfig installs data as the unit's config and as its last known good copy.
func writeUnitConfig(cfg *Config, data []byte, coreVersion string) error {
	if err := writeFileAtomic(unitConfigPath(), data, 0o640); err != nil {
		return err
	}
	if err := writeFileAtomic(unitLastGoodPath(), data, 0o640); err != nil {
		return err
	}
	return writeUnitMeta(cfg, coreVersion)
}

// startUnit tests the config, installs it and restarts the unit; a config that passes the test
// but does not start is replaced by the last good one, which is started again.
func (p *process) startUnit() error {
	data, err := json.MarshalIndent(p.config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to generate xray config: %w", err)
	}
	if err := makeUnitFolder(config.GetXrayUnitFolder()); err != nil {
		return err
	}
	if err := UnitListenError(p.config); err != nil {
		return fmt.Errorf("%w: %w", ErrConfigRejected, err)
	}
	// `xray run -test` runs as root and would pass; the core and the rollback would both fail.
	if err := unreadableAssets(data); err != nil {
		return fmt.Errorf("%w: %w", ErrConfigRejected, err)
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
		p.refreshVersion()
		if err := writeUnitMeta(p.config, p.GetXrayVersion()); err != nil {
			logger.Warning("xray unit: could not record config metadata:", err)
		}
		pruneCertMirrors(p.config)
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
	st := u.state(true)
	if !st.running() {
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
	meta := readUnitMeta()
	applyUnitMeta(cfg, meta)
	p := &Process{newProcess(cfg)}
	p.unit = u
	p.adoptedRelayPasswords = meta.RelayPasswords
	p.refreshVersion()
	if installed := p.GetXrayVersion(); binaryReplacedSince(st) && meta.CoreVersion != installed {
		p.mu.Lock()
		p.version, p.installedVersion = cmp.Or(meta.CoreVersion, "Unknown"), installed
		p.mu.Unlock()
	}
	p.refreshAPIPort()
	return p
}

// ReadoptRestartedUnit adopts the core systemd restarted by itself. While that core is still
// activating it returns nil, and TakeUnitRestart reports the restart again on the next tick.
func ReadoptRestartedUnit() *Process {
	p := AdoptRunningUnit()
	if p == nil && newUnitControl(config.GetXrayUnit()).state(true).Active == "activating" {
		unitRestarted.Store(true)
	}
	return p
}

// binaryReplacedSince reports a core binary installed after the unit's core started: the
// core still runs the old, deleted file. The panel cannot read /proc/<pid>/exe without CAP_SYS_PTRACE.
func binaryReplacedSince(st unitState) bool {
	info, err := os.Stat(GetBinaryPath())
	if err != nil || st.StartMono == 0 {
		return false
	}
	raw, err := os.ReadFile("/proc/uptime")
	fields := strings.Fields(string(raw))
	if err != nil || len(fields) == 0 {
		return false
	}
	uptime, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return false
	}
	sinceStart := time.Duration(uptime*float64(time.Second)) - time.Duration(st.StartMono)*time.Microsecond
	return fileChangedAt(info).After(time.Now().Add(-sinceStart).Add(time.Second))
}

// CoreRestartNeeded names the core binary installed while the unit's core kept running the
// previous one; empty when the running core is the installed one.
func (p *Process) CoreRestartNeeded() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.installedVersion == "" {
		return ""
	}
	return fmt.Sprintf("Xray %s is installed but the running core is still %s: restart Xray to run it", p.installedVersion, p.version)
}

// makeUnitFolder creates a missing folder the core reads through its group; an existing one is
// left alone, as chmod would clear the setgid bit RestrictSUIDSGID forbids the panel to set again.
func makeUnitFolder(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	return mkdirAllGroupReadable(dir)
}

// unitSocketFolders are where the unit's core can create a unix socket other services reach:
// its RuntimeDirectory, and /dev/shm, which PrivateDevices shares with the host.
var unitSocketFolders = []string{"/run/still-xray/", "/dev/shm/"}

// UnitListenError refuses an inbound listening on a unix socket the unit's sandbox hides
// (PrivateTmp) or makes read-only (ProtectSystem=strict); in 3x-ui's child mode any path works.
func UnitListenError(cfg *Config) error {
	if cfg == nil || !UnitMode() {
		return nil
	}
	for _, ib := range cfg.InboundConfigs {
		if p := unitSocketProblem(ib); p != "" {
			return errors.New(p)
		}
	}
	return nil
}

// extAssetRef matches the external geo files a config names (ext:file.dat:tag).
var extAssetRef = regexp.MustCompile(`"ext(?:-ip|-domain)?:([^:"/]+):`)

// unreadableAssets reports a geo file the config uses that the unit's core cannot read.
func unreadableAssets(data []byte) error {
	var names []string
	if bytes.Contains(data, []byte("geoip:")) {
		names = append(names, "geoip.dat")
	}
	if bytes.Contains(data, []byte("geosite:")) {
		names = append(names, "geosite.dat")
	}
	for _, m := range extAssetRef.FindAllSubmatch(data, -1) {
		names = append(names, string(m[1]))
	}
	folder, err := os.Stat(config.GetXrayUnitFolder())
	if err != nil {
		return nil
	}
	for _, name := range names {
		path := filepath.Join(config.GetBinFolderPath(), name)
		info, err := os.Stat(path)
		if err != nil {
			continue // a missing file is for `xray run -test` to report
		}
		if !readableByGroupOf(info, folder) {
			return fmt.Errorf("%s is not readable by the still-xray service (mode %o): run chmod a+r %s", path, info.Mode().Perm(), path)
		}
	}
	return nil
}

// AdoptedRelayPasswords returns the relay passwords recorded with an adopted core's config.
func (p *Process) AdoptedRelayPasswords() map[string]string { return p.adoptedRelayPasswords }

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
