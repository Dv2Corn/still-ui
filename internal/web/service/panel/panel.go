package panel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/version"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// PanelService provides business logic for panel management operations.
// It handles panel restart, updates, and system-level panel controls.
type PanelService struct{}

// PanelUpdateInfo contains the current and latest available panel versions.
// Channel is always "stable": still-ui publishes no dev builds. The commit
// fields are kept for API compatibility and stay empty.
type PanelUpdateInfo struct {
	Channel         string `json:"channel"`
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion"`
	CurrentCommit   string `json:"currentCommit,omitempty"`
	LatestCommit    string `json:"latestCommit,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
}

const (
	// releaseDownloadBase is where release files live: <base>/<tag>/<name>.
	// The updater is a release file listed in that release's SHA256SUMS, so
	// the script that runs as root is the one the release build published and
	// attested, never a branch head.
	releaseDownloadBase  = "https://github.com/" + config.ReleaseRepo + "/releases/download"
	panelUpdaterName     = "update.sh"
	releaseSumsName      = "SHA256SUMS"
	maxPanelUpdaterBytes = 2 << 20
	maxReleaseSumsBytes  = 64 << 10
	maxPanelReleaseBytes = 2 << 20

	updateStatePending = "pending"
	updateStateSuccess = "success"
	updateStateFailed  = "failed"
)

// PanelUpdateStatus reports the outcome of the most recently launched panel
// self-update. RunID lets the caller confirm this status belongs to the
// update it started rather than a stale result left over from an earlier
// run; State is one of "pending", "success", or "failed". RunID is a decimal
// string, not a JSON number: it's a formatted UnixNano timestamp, and
// JavaScript's number type can't represent that precisely (it exceeds
// Number.MAX_SAFE_INTEGER), which would let two different runs round to the
// same value on the wire and defeat the whole point of this field.
type PanelUpdateStatus struct {
	RunID      string `json:"runId" example:"1735689600123456789"`
	State      string `json:"state" example:"success"`
	ExitCode   int    `json:"exitCode" example:"0"`
	FinishedAt int64  `json:"finishedAt" example:"1735689612"`
}

// releaseTagRegex accepts still-ui release tags (v1.5.0, v3.11.0-still.5).
// The tag ends up in a URL and in update.sh's environment.
var releaseTagRegex = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// ErrDevChannelUnavailable is returned for a dev-channel update: still-ui
// publishes no rolling dev build, every update installs a release.
var ErrDevChannelUnavailable = errors.New("still-ui publishes no dev builds; the panel updates to the latest release")

// updateMu guards updateRunning/updateStarted/updateRunID/updatePID, which
// stop a second self-update from launching while one is still in flight (two
// concurrent update.sh runs would race each other extracting the release
// tarball and swapping the service unit). A slot is released as soon as the
// in-flight run's own status file reports success or failure -- checked
// against updateRunID so a stale file from an even earlier run can't be
// mistaken for this one finishing -- so a fast failure doesn't lock out a
// retry.
//
// For a run that never reaches a terminal state at all, staleness is judged
// primarily by whether the process we actually launched is still alive
// (updatePID, via processAlive), not by wall-clock time alone: update.sh
// runs install_base() (a package-manager update+install) before anything
// else, plus several downloads, which can legitimately run past a short
// fixed timeout on a slow or throttled host without anything being wrong.
// updateStaleAfter/updatePID together are only a fallback for the systemd-run
// launch path, where the process we can observe (systemd-run itself) has
// already exited by the time startUpdate returns and the actual update.sh
// unit's PID is never recorded -- for that path this is still a pure
// wall-clock heuristic. updateHardCeiling is an absolute backstop so a
// genuinely wedged run (alive but hung forever) can never lock out retries
// permanently, even on the PID-tracked path.
var (
	updateMu      sync.Mutex
	updateRunning bool
	updateStarted time.Time
	updateRunID   int64
	updatePID     int
)

const (
	updateStaleAfter  = 20 * time.Minute
	updateHardCeiling = 2 * time.Hour
)

func (s *PanelService) RestartPanel(delay time.Duration) error {
	go func() {
		time.Sleep(delay)
		if global.TriggerRestart() {
			return
		}
		if runtime.GOOS == "windows" {
			logger.Error("panel restart: no restart hook registered (SIGHUP unsupported on Windows)")
			return
		}
		p, err := os.FindProcess(syscall.Getpid())
		if err != nil {
			logger.Error("panel restart: FindProcess failed:", err)
			return
		}
		if err := p.Signal(syscall.SIGHUP); err != nil {
			logger.Error("failed to send SIGHUP signal:", err)
		}
	}()
	return nil
}

// GetUpdateInfo checks GitHub for the latest still-ui release and compares
// it with the running version. A dev-channel setting left on by an older
// release is ignored: still-ui publishes no dev builds.
func (s *PanelService) GetUpdateInfo() (*PanelUpdateInfo, error) {
	latest, err := fetchLatestPanelVersion()
	if err != nil {
		return nil, err
	}
	current := config.GetBaseVersion()
	return &PanelUpdateInfo{
		Channel:         "stable",
		CurrentVersion:  current,
		LatestVersion:   latest,
		UpdateAvailable: isNewerVersion(latest, current),
	}, nil
}

// StartUpdate starts the official updater for the latest release. Returns the
// run ID to pass to GetUpdateStatus so the caller can tell this run's result
// apart from a stale one.
func (s *PanelService) StartUpdate() (int64, error) {
	return s.startUpdate()
}

// StartUpdateChannel is what the master node updater calls on a node. Only
// the stable channel exists; a dev request is refused before anything runs.
func (s *PanelService) StartUpdateChannel(dev bool) (int64, error) {
	if dev {
		return 0, ErrDevChannelUnavailable
	}
	return s.startUpdate()
}

// GetUpdateStatus reports the outcome of the most recently launched panel
// self-update, as recorded by update.sh's EXIT trap (see the script for why
// that covers every exit path, not just the happy one). This is a best-effort
// side channel: a missing or unreadable status file reads as "pending"
// rather than an error, since the update itself is what matters, not this
// status file.
func (s *PanelService) GetUpdateStatus() *PanelUpdateStatus {
	data, err := os.ReadFile(config.GetUpdateStatusFilePath())
	if err != nil {
		return &PanelUpdateStatus{State: updateStatePending}
	}
	var status PanelUpdateStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return &PanelUpdateStatus{State: updateStatePending}
	}
	if status.State != updateStateSuccess && status.State != updateStateFailed {
		status.State = updateStatePending
	}
	return &status
}

func (s *PanelService) startUpdate() (int64, error) {
	runID := time.Now().UnixNano()
	if !acquireUpdateSlot(runID) {
		return 0, fmt.Errorf("a panel update is already in progress")
	}
	launched := false
	defer func() {
		if !launched {
			releaseUpdateSlot()
		}
	}()

	if runtime.GOOS != "linux" {
		return 0, fmt.Errorf("panel web update is supported only on Linux installations")
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		return 0, fmt.Errorf("bash is required to run the panel updater: %w", err)
	}

	// The updater of the release it installs: update.sh from that release,
	// checked against the release's SHA256SUMS, and XUI_UPDATE_TAG makes it
	// install that same release.
	updateTag, err := fetchLatestPanelVersion()
	if err != nil {
		return 0, fmt.Errorf("find the latest release: %w", err)
	}
	client := (&service.SettingService{}).NewProxiedHTTPClient(30 * time.Second)
	scriptPath, err := downloadPanelUpdater(context.Background(), client, releaseDownloadBase, updateTag)
	if err != nil {
		return 0, err
	}

	statusFile := config.GetUpdateStatusFilePath()

	mainFolder, serviceFolder := resolveUpdateFolders()
	updateScript := fmt.Sprintf("set -e; trap 'rm -f %s' EXIT; %s %s", shellQuote(scriptPath), shellQuote(bash), shellQuote(scriptPath))
	runIDEnv := "XUI_UPDATE_RUN_ID=" + strconv.FormatInt(runID, 10)
	statusFileEnv := "XUI_UPDATE_STATUS_FILE=" + statusFile
	// update.sh sends its curl downloads through the panel egress bridge. With socks5h the
	// proxy resolves GitHub's hostnames, since this server's own DNS may be filtered.
	egressEnv := "XUI_UPDATE_PROXY=" + strings.Replace((&service.SettingService{}).PanelEgressProxyURL(), "socks5://", "socks5h://", 1)
	proxyEnv := updateProxyEnvVars()

	if systemdRun, err := exec.LookPath("systemd-run"); err == nil {
		unitName := fmt.Sprintf("x-ui-web-update-%d", time.Now().Unix())
		args := []string{
			"--unit", unitName,
			"--setenv", "XUI_MAIN_FOLDER=" + mainFolder,
			"--setenv", "XUI_SERVICE=" + serviceFolder,
			"--setenv", "XUI_UPDATE_TAG=" + updateTag,
			"--setenv", runIDEnv,
			"--setenv", statusFileEnv,
			"--setenv", egressEnv,
		}
		for _, kv := range proxyEnv {
			args = append(args, "--setenv", kv)
		}
		args = append(args, bash, "-lc", updateScript)
		cmd := exec.CommandContext(context.Background(), systemdRun, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			output := strings.TrimSpace(string(out))
			if !strings.Contains(output, "System has not been booted with systemd") &&
				!strings.Contains(output, "Failed to connect to bus") {
				_ = os.Remove(scriptPath)
				return 0, fmt.Errorf("failed to start panel update job: %w: %s", err, output)
			}
			logger.Warning("systemd-run is unavailable, falling back to detached update process:", output)
		} else {
			logger.Infof("started panel update job via systemd-run unit %s", unitName)
			launched = true
			return runID, nil
		}
	}

	cmd := exec.CommandContext(context.Background(), bash, "-lc", updateScript)
	cmd.Env = append(os.Environ(),
		"XUI_MAIN_FOLDER="+mainFolder,
		"XUI_SERVICE="+serviceFolder,
		"XUI_UPDATE_TAG="+updateTag,
		runIDEnv,
		statusFileEnv,
		egressEnv,
	)
	setDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.Remove(scriptPath)
		return 0, fmt.Errorf("failed to start panel update job: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		logger.Warning("failed to release panel update process:", err)
	}
	logger.Infof("started panel update job with pid %d", cmd.Process.Pid)
	recordUpdatePID(cmd.Process.Pid)
	launched = true
	return runID, nil
}

// updateProxyEnvVars forwards ambient proxy env vars to systemd-run's child,
// which (unlike the bash fallback) inherits nothing but --setenv.
func updateProxyEnvVars() []string {
	var out []string
	for _, key := range []string{"https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY", "http_proxy", "HTTP_PROXY", "no_proxy", "NO_PROXY"} {
		if v := os.Getenv(key); v != "" {
			out = append(out, key+"="+v)
		}
	}
	return out
}

// acquireUpdateSlot claims the single in-flight-update slot for runID. It
// refuses while another run is genuinely still in flight, but grants the
// slot immediately once that run's own status file reports a terminal
// result (success or failure) -- a fast failure shouldn't force the next
// attempt to wait out updateStaleAfter for no reason. Past updateStaleAfter
// with no terminal status yet, it grants the slot anyway UNLESS the process
// we recorded (updatePID) is confirmed still alive, so a merely-slow run
// isn't mistaken for a crashed one; past updateHardCeiling it grants the
// slot unconditionally regardless of liveness, so a truly wedged run can
// never lock out retries forever.
func acquireUpdateSlot(runID int64) bool {
	updateMu.Lock()
	defer updateMu.Unlock()
	if updateRunning && !previousRunIsTerminal() {
		elapsed := time.Since(updateStarted)
		if elapsed < updateHardCeiling {
			stale := elapsed >= updateStaleAfter
			alive := updatePID > 0 && processAlive(updatePID)
			if !stale || alive {
				return false
			}
		}
	}
	updateRunning = true
	updateStarted = time.Now()
	updateRunID = runID
	updatePID = 0
	return true
}

// recordUpdatePID notes the PID of the detached update.sh process the
// current slot is tracking, so a later acquireUpdateSlot call can check
// whether it is actually still running instead of only how long ago it
// started. Only reachable for the detached-fallback launch path -- the
// systemd-run path never learns update.sh's own PID, since the process it
// directly observes (systemd-run) has already exited by the time it returns.
func recordUpdatePID(pid int) {
	updateMu.Lock()
	updatePID = pid
	updateMu.Unlock()
}

// previousRunIsTerminal reports whether the run currently recorded in
// updateRunID has reached success or failure per its status file. Must be
// called with updateMu held.
func previousRunIsTerminal() bool {
	status := (&PanelService{}).GetUpdateStatus()
	return status.RunID == strconv.FormatInt(updateRunID, 10) && status.State != updateStatePending
}

func releaseUpdateSlot() {
	updateMu.Lock()
	updateRunning = false
	updateMu.Unlock()
}

// downloadPanelUpdater downloads update.sh of release tag from base and
// returns the path of a 0700 temp file holding it. The script must match the
// sha256 the release's SHA256SUMS lists for it; a missing SHA256SUMS, a
// missing entry or a mismatch refuse the update and leave no file behind.
func downloadPanelUpdater(ctx context.Context, client *http.Client, base, tag string) (string, error) {
	if !releaseTagRegex.MatchString(tag) {
		return "", fmt.Errorf("download panel updater: %q is not a release tag", tag)
	}
	releaseBase := strings.TrimRight(base, "/") + "/" + tag + "/"

	sums, err := fetchReleaseFile(ctx, client, releaseBase+releaseSumsName, maxReleaseSumsBytes)
	if err != nil {
		return "", fmt.Errorf("download %s of %s: %w; refusing to run an unverified updater", releaseSumsName, tag, err)
	}
	want, ok := releaseFileSum(sums, panelUpdaterName)
	if !ok {
		return "", fmt.Errorf("%s of %s does not list %s; refusing to run an unverified updater", releaseSumsName, tag, panelUpdaterName)
	}

	script, err := fetchReleaseFile(ctx, client, releaseBase+panelUpdaterName, maxPanelUpdaterBytes)
	if err != nil {
		return "", fmt.Errorf("download panel updater: %w", err)
	}
	if len(script) == 0 {
		return "", fmt.Errorf("panel updater download is empty")
	}
	sum := sha256.Sum256(script)
	if got := hex.EncodeToString(sum[:]); got != want {
		return "", fmt.Errorf("panel updater checksum mismatch: %s of %s lists %s, the download is %s", releaseSumsName, tag, want, got)
	}

	file, err := os.CreateTemp("", "still-ui-update-*.sh")
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok = false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(script); err != nil {
		return "", fmt.Errorf("write panel updater: %w", err)
	}
	if err := file.Chmod(0o700); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

// fetchReleaseFile GETs url and returns its body, refusing anything but
// HTTP 200 and bodies over limit bytes.
func fetchReleaseFile(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return body, nil
}

// releaseFileSum returns the sha256 a SHA256SUMS file (sha256sum format,
// "<hex>  <name>" or "<hex> *<name>") lists for name.
func releaseFileSum(sums []byte, name string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		sum := strings.ToLower(fields[0])
		if len(sum) != sha256.Size*2 {
			return "", false
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return "", false
		}
		return sum, true
	}
	return "", false
}

func fetchLatestPanelVersion() (string, error) {
	release, err := fetchLatestPanelRelease()
	if err != nil {
		return "", err
	}
	if !releaseTagRegex.MatchString(release.TagName) {
		return "", fmt.Errorf("latest panel release tag %q is not a release tag", release.TagName)
	}
	return release.TagName, nil
}

// fetchLatestPanelRelease fetches the latest stable release from GitHub.
func fetchLatestPanelRelease() (*service.Release, error) {
	url := "https://api.github.com/repos/" + config.ReleaseRepo + "/releases/latest"
	client := (&service.SettingService{}).NewProxiedHTTPClient(10 * time.Second)
	req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if reqErr != nil {
		return nil, reqErr
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, resp.Status)
	}

	var release service.Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPanelReleaseBytes)).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

func resolveUpdateFolders() (string, string) {
	mainFolder := os.Getenv("XUI_MAIN_FOLDER")
	if mainFolder == "" {
		if exePath, err := os.Executable(); err == nil {
			mainFolder = filepath.Dir(exePath)
		}
	}
	if mainFolder == "" {
		mainFolder = config.DefaultMainFolder
	}

	serviceFolder := os.Getenv("XUI_SERVICE")
	if serviceFolder == "" {
		serviceFolder = "/etc/systemd/system"
	}
	return mainFolder, serviceFolder
}

func isNewerVersion(latest string, current string) bool {
	cmp, ok := version.Compare(latest, current)
	if !ok {
		return version.Normalize(latest) != version.Normalize(current)
	}
	return cmp > 0
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
