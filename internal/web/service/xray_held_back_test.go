package service

import (
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
