package service

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// The settings controller keeps one XrayService for every request, and balancer
// status and route tests initialised and closed its single API client without a
// lock: two concurrent requests raced on it and one's Close cut off the other.
func TestBalancerStatusAndRouteTestDoNotShareAnAPIClient(t *testing.T) {
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
	if process.GetAPIPort() <= 0 {
		t.Fatal("stand-in core has no API port")
	}
	previousProcess, previousResult := xrayState.snapshot()
	xrayState.replace(process)
	t.Cleanup(func() {
		_ = process.Stop()
		xrayState.mu.Lock()
		xrayState.process, xrayState.result = previousProcess, previousResult
		xrayState.mu.Unlock()
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = svc.GetBalancersStatus([]string{"b1"})
		}()
		go func() {
			defer wg.Done()
			_, _ = svc.TestRoute(xray.RouteTestRequest{Domain: "example.com", Port: 443})
		}()
	}
	wg.Wait()
}
