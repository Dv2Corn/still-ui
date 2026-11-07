package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// A refusal held the previous config back and said so on the status page; once
// the desired config is back to the one the core runs (the change was reverted),
// RestartXray returned early and the stale refusal stayed there for good.
func TestRestartXrayClearsTheRefusalOnceTheCoreRunsTheDesiredConfig(t *testing.T) {
	setupConflictDB(t)
	bin := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", bin)
	t.Setenv("XUI_LOG_FOLDER", t.TempDir())
	script := "#!/bin/sh\n[ \"$1\" = -version ] && { echo 'Xray 26.1.1 x'; exit 0; }\nexec sleep 120\n"
	if err := os.WriteFile(filepath.Join(bin, xray.GetBinaryName()), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	svc := &XrayService{}
	cfg, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	process := xray.NewProcess(cfg)
	if err := process.Start(); err != nil {
		t.Fatalf("start stand-in core: %v", err)
	}
	previousProcess, previousResult := xrayState.snapshot()
	previousRestart := isNeedXrayRestart.Load()
	xrayState.replace(process)
	isNeedXrayRestart.Store(false)
	t.Cleanup(func() {
		_ = process.Stop()
		isNeedXrayRestart.Store(previousRestart)
		xrayState.mu.Lock()
		xrayState.process, xrayState.result, xrayState.heldBack = previousProcess, previousResult, ""
		xrayState.mu.Unlock()
	})

	xrayState.holdBack("config refused: port 443 is taken")
	if err := svc.RestartXray(false); err != nil {
		t.Fatalf("RestartXray: %v", err)
	}
	if currentXrayProcess() != process {
		t.Fatal("an unchanged config replaced the running core")
	}
	if reason := svc.GetHeldBackConfig(); reason != "" {
		t.Fatalf("status still says %q while the core runs the desired config", reason)
	}
}

// saveXrayState puts the package's core state back after a test that changes it.
func saveXrayState(t *testing.T) {
	t.Helper()
	xrayState.mu.Lock()
	process, result, heldBack, refused, refusedErr := xrayState.process, xrayState.result, xrayState.heldBack, xrayState.refused, xrayState.refusedErr
	xrayState.mu.Unlock()
	t.Cleanup(func() {
		xrayState.mu.Lock()
		xrayState.process, xrayState.result, xrayState.heldBack, xrayState.refused, xrayState.refusedErr = process, result, heldBack, refused, refusedErr
		xrayState.mu.Unlock()
	})
}

// Config A was refused, then B started; going back to A skipped the retry and
// returned A's old refusal while the core served B.
func TestStartingACoreForgetsTheLastRefusal(t *testing.T) {
	setupConflictDB(t)
	bin := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", bin)
	t.Setenv("XUI_LOG_FOLDER", t.TempDir())
	script := "#!/bin/sh\n[ \"$1\" = -version ] && { echo 'Xray 26.1.1 x'; exit 0; }\nexec sleep 120\n"
	if err := os.WriteFile(filepath.Join(bin, xray.GetBinaryName()), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	saveXrayState(t)
	previousStopped := isManuallyStopped.Load()
	t.Cleanup(func() {
		if process := currentXrayProcess(); process != nil {
			_ = process.Stop()
		}
		isManuallyStopped.Store(previousStopped)
	})

	xrayState.refuse(&xray.Config{LogConfig: []byte(`{"loglevel":"warning"}`)}, xray.ErrConfigRejected)
	if err := (&XrayService{}).RestartXray(true); err != nil {
		t.Fatalf("RestartXray: %v", err)
	}
	if cfg, err := xrayState.lastRefused(); cfg != nil || err != nil {
		t.Fatalf("after a requested config started, lastRefused = %v, %v; want nil, nil", cfg, err)
	}
}

// Re-adopting the core systemd restarted with the last good config dropped the
// refusal, so the next reconcile retried the refused config and cut clients off again.
func TestReadoptAfterACrashKeepsTheRefusal(t *testing.T) {
	saveXrayState(t)
	previousReadopt := readoptRestartedUnit
	readoptRestartedUnit = func() *xray.Process { return xray.NewProcess(&xray.Config{}) }
	t.Cleanup(func() { readoptRestartedUnit = previousReadopt })

	refusedCfg := &xray.Config{LogConfig: []byte(`{"loglevel":"warning"}`)}
	xrayState.refuse(refusedCfg, xray.ErrConfigRejected)
	if !(&XrayService{}).readoptXrayUnit() {
		t.Fatal("readoptXrayUnit did not take the restarted core")
	}
	if cfg, err := xrayState.lastRefused(); cfg != refusedCfg || !errors.Is(err, xray.ErrConfigRejected) {
		t.Fatalf("after re-adopting a restarted core, lastRefused = %v, %v; want the refused config", cfg, err)
	}
}
