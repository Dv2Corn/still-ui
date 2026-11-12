package job

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/connlog"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func writeLogConfig(t *testing.T, accessPath string, errorPath string) {
	t.Helper()
	binDir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", binDir)
	configData, err := json.Marshal(map[string]any{
		"log": map[string]any{"access": accessPath, "error": errorPath},
	})
	if err != nil {
		t.Fatalf("marshal xray config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "config.json"), configData, 0o644); err != nil {
		t.Fatalf("write xray config: %v", err)
	}
}

func TestWipeXrayLogs_TruncatesEnabledLogs(t *testing.T) {
	// Emptying the access log reads the connection log settings, so it needs a database.
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	accessLog := filepath.Join(t.TempDir(), "access.log")
	errorLog := filepath.Join(t.TempDir(), "error.log")
	if err := os.WriteFile(accessLog, []byte("2026/06/23 12:00:00 from tcp:203.0.113.10:443 accepted\n"), 0o644); err != nil {
		t.Fatalf("seed access log: %v", err)
	}
	if err := os.WriteFile(errorLog, []byte("xray warning\n"), 0o644); err != nil {
		t.Fatalf("seed error log: %v", err)
	}
	writeLogConfig(t, accessLog, errorLog)

	wipeXrayLogs()

	for _, logPath := range []string{accessLog, errorLog} {
		info, err := os.Stat(logPath)
		if err != nil {
			t.Fatalf("%s should still exist: %v", logPath, err)
		}
		if info.Size() != 0 {
			t.Fatalf("%s should be truncated to 0, got %d bytes", logPath, info.Size())
		}
	}
}

func TestWipeXrayLogs_LeavesDisabledLogsAlone(t *testing.T) {
	writeLogConfig(t, "none", "none")

	wipeXrayLogs()

	if _, err := os.Stat("none"); err == nil {
		os.Remove("none")
		t.Fatal(`wipeXrayLogs must not create a file named "none"`)
	}
}

func TestPruneXrayLogs_TruncatesOnlyOversizedLogs(t *testing.T) {
	oldMax := maxXrayLogBytes
	maxXrayLogBytes = 8
	defer func() { maxXrayLogBytes = oldMax }()

	dir := t.TempDir()
	accessLog := filepath.Join(dir, "access.log")
	errorLog := filepath.Join(dir, "error.log")
	if err := os.WriteFile(accessLog, []byte("small"), 0o644); err != nil {
		t.Fatalf("seed access log: %v", err)
	}
	if err := os.WriteFile(errorLog, []byte("large log line"), 0o644); err != nil {
		t.Fatalf("seed error log: %v", err)
	}
	writeLogConfig(t, accessLog, errorLog)

	NewPruneXrayLogsJob().Run()

	accessInfo, err := os.Stat(accessLog)
	if err != nil {
		t.Fatalf("access log should still exist: %v", err)
	}
	if accessInfo.Size() != 5 {
		t.Fatalf("small access log should be left alone, got %d bytes", accessInfo.Size())
	}
	errorInfo, err := os.Stat(errorLog)
	if err != nil {
		t.Fatalf("error log should still exist: %v", err)
	}
	if errorInfo.Size() != 0 {
		t.Fatalf("oversized error log should be truncated, got %d bytes", errorInfo.Size())
	}
}

// The connection log reads the access log; the panel must hand it the unread lines
// before it empties the file, and must not disturb the error log's truncation.
func TestWipeXrayLogs_StoresUnreadAccessLinesInTheConnectionLog(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	dbtest.InitDB(t, config.GetDBPath())
	accessLog, errorLog := filepath.Join(dir, "access.log"), filepath.Join(dir, "error.log")
	writeLogConfig(t, accessLog, errorLog)
	for _, path := range []string{accessLog, errorLog} {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	settings := &service.SettingService{}
	all, err := settings.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	all.ConnLogEnable = true
	if err := settings.UpdateAllSetting(all, service.SecretClears{}); err != nil {
		t.Fatal(err)
	}
	connLog := &service.ConnLogService{}
	connLog.Tick()
	t.Cleanup(func() {
		all.ConnLogEnable = false
		_ = settings.UpdateAllSetting(all, service.SecretClears{})
		connLog.Tick()
	})

	line := time.Now().Format("2006/01/02 15:04:05.000000") + " from 203.0.113.10:443 accepted tcp:example.com:443 [in-1 >> direct] email: alice\n"
	if err := os.WriteFile(accessLog, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	wipeXrayLogs()

	if info, err := os.Stat(accessLog); err != nil || info.Size() != 0 {
		t.Fatalf("access log after the wipe: %v, err %v", info, err)
	}
	page, err := connLog.Query(context.Background(), connlog.ConnFilter{})
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Client != "alice" || page.Rows[0].Host != "example.com" {
		t.Fatalf("connection log after the wipe: %+v, err %v", page, err)
	}
}
