package service

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/connlog"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	xuilogger "github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"

	"github.com/op/go-logging"
)

// connLogPanel is a panel on a throwaway database whose Xray config names accessLog.
func connLogPanel(t *testing.T) (accessLog string) {
	t.Helper()
	portConflictLoggerOnce.Do(func() { xuilogger.InitLogger(logging.ERROR) })
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	dbtest.InitDB(t, config.GetDBPath())
	accessLog = filepath.Join(dir, "access.log")
	configJSON, _ := json.Marshal(map[string]any{"log": map[string]any{"access": accessLog}})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), configJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	connLogCollector, connLogPathStamp, connLogLastFailure = &connlog.Collector{}, nil, ""
	t.Cleanup(func() {
		_ = connLogCollector.Halt(context.Background(), connlog.Config{Dir: config.GetConnLogFolderPath(), RetentionDays: 1, MaxBytes: 1 << 30}, time.Now())
	})
	return accessLog
}

func setConnLog(t *testing.T, enable bool, destination, source string) {
	t.Helper()
	s := &SettingService{}
	all, err := s.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	all.ConnLogEnable, all.ConnLogDestination, all.ConnLogSource = enable, destination, source
	if err := s.UpdateAllSetting(all, SecretClears{}); err != nil {
		t.Fatal(err)
	}
}

func generatedAccessLog(t *testing.T) any {
	t.Helper()
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	var logCfg map[string]any
	if err := json.Unmarshal(cfg.LogConfig, &logCfg); err != nil {
		t.Fatal(err)
	}
	return logCfg["access"]
}

func appendAccessLog(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

// Off is the default and must cost nothing: Xray's access log stays as the operator
// set it and no file appears on disk.
func TestConnLogOffChangesNothing(t *testing.T) {
	accessLog := connLogPanel(t)
	appendAccessLog(t, accessLog, "2024/01/02 15:04:05.000000 from 1.2.3.4:5 accepted tcp:example.com:443 [in-1 >> direct] email: alice\n")

	if got := generatedAccessLog(t); got != "none" {
		t.Errorf("generated access log = %v, want the template's \"none\"", got)
	}
	svc := &ConnLogService{}
	svc.Tick()
	svc.Tick()
	if _, err := os.Stat(config.GetConnLogFolderPath()); !os.IsNotExist(err) {
		t.Errorf("the log folder exists with the log off (stat error: %v)", err)
	}
	page, err := svc.Query(context.Background(), connlog.ConnFilter{})
	if err != nil || len(page.Rows) != 0 || page.Enabled || page.SizeBytes != 0 {
		t.Errorf("query with the log off: %+v, err %v", page, err)
	}
}

func TestConnLogOnTurnsTheAccessLogOnOnlyWhenTheOperatorHasNone(t *testing.T) {
	connLogPanel(t)
	setConnLog(t, true, connlog.DestinationFull, connlog.SourceFull)
	ours := filepath.Join(config.GetLogFolder(), connLogAccessLogName)
	if got := generatedAccessLog(t); got != ours {
		t.Errorf("generated access log = %v, want %s", got, ours)
	}

	tests := []struct{ name, in, want string }{
		{"none", `{"access":"none","loglevel":"warning"}`, ours},
		{"empty", `{"access":""}`, ours},
		{"absent key", `{"loglevel":"warning"}`, ours},
		{"no log section", ``, ours},
		{"a null log section", `null`, ours},
		{"the operator's own log is kept", `{"access":"/var/log/mine.log"}`, "/var/log/mine.log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]any
			if err := json.Unmarshal(ensureAccessLog(json_util.RawMessage(tt.in)), &got); err != nil {
				t.Fatal(err)
			}
			if got["access"] != tt.want {
				t.Errorf("access = %v, want %s", got["access"], tt.want)
			}
			if strings.Contains(tt.in, "loglevel") && got["loglevel"] != "warning" {
				t.Errorf("the rest of the log section was lost: %v", got)
			}
		})
	}
}

// The whole path: real Xray lines in the access log, the cron step, a query, a policy
// change, the panel's own log truncation and the CSV export.
func TestConnLogCollectsQueriesAndExports(t *testing.T) {
	accessLog := connLogPanel(t)
	setConnLog(t, true, connlog.DestinationFull, connlog.SourceFull)
	svc := &ConnLogService{}
	appendAccessLog(t, accessLog, "")
	svc.Tick()

	raw, err := os.ReadFile("../../xray/testdata/access_log_real.txt")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's own date is long past the retention: move its lines to now.
	stamp := time.Now().Format("2006/01/02 15:04:05.000000")
	var lines strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		// The line a request wrote for itself keeps its own date, years ahead: it must not be stored.
		if strings.HasPrefix(line, "2030/") {
			lines.WriteString(line + "\n")
			continue
		}
		lines.WriteString(stamp + line[len(stamp):] + "\n")
	}
	appendAccessLog(t, accessLog, lines.String())
	svc.Tick()

	page, err := svc.Query(context.Background(), connlog.ConnFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 10 || !page.Enabled || page.SizeBytes == 0 || page.Next != "" {
		t.Fatalf("got %d rows, enabled %v, %d bytes, next %q; want the fixture's 10 connections", len(page.Rows), page.Enabled, page.SizeBytes, page.Next)
	}
	page, err = svc.Query(context.Background(), connlog.ConnFilter{Client: "alice@example.com"})
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Inbound != "in-vless" || page.Rows[0].Host != "127.0.0.1" || page.Rows[0].Source != "127.0.0.1" || page.Rows[0].SourcePort != 38588 {
		t.Fatalf("alice's connections: %+v, err %v", page.Rows, err)
	}

	// A stricter policy applies to what comes next and leaves what is stored alone.
	setConnLog(t, true, connlog.DestinationDomain, connlog.SourceMasked)
	appendAccessLog(t, accessLog, stamp+" from 203.0.113.77:1234 accepted tcp:=cmd.maps.example.co.uk:443 [in-vless >> direct] email: bob\n")
	if err := TruncateAccessLog(accessLog, func() error { return os.Truncate(accessLog, 0) }); err != nil {
		t.Fatal(err)
	}
	page, err = svc.Query(context.Background(), connlog.ConnFilter{Client: "bob"})
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Host != "example.co.uk" || page.Rows[0].Source != "203.0.113.0/24" || page.Rows[0].SourcePort != 0 {
		t.Fatalf("bob's connection, logged just before the truncation: %+v, err %v", page.Rows, err)
	}
	if page, _ = svc.Query(context.Background(), connlog.ConnFilter{}); len(page.Rows) != 11 {
		t.Fatalf("%d rows after the policy change, want the 10 older ones kept and 1 new", len(page.Rows))
	}

	var out bytes.Buffer
	if err := svc.Export(context.Background(), &out, connlog.ConnFilter{Inbound: "in-vless"}); err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(out.String()), "\n")
	utc := time.Now().UTC().Format("2006-01-02T")
	if len(rows) != 3 || rows[0] != "time_utc,client,inbound,outbound,source,source_port,network,host,port" ||
		!strings.HasPrefix(rows[1], utc) || !strings.HasSuffix(rows[1], ",bob,in-vless,direct,203.0.113.0/24,0,tcp,example.co.uk,443") ||
		!strings.HasSuffix(rows[2], ",alice@example.com,in-vless,direct,127.0.0.1,38588,tcp,127.0.0.1,42899") {
		t.Errorf("export:\n%s", out.String())
	}
}

// A destination is the client's text; a spreadsheet must not run it as a formula.
func TestConnLogExportDefusesFormulaCells(t *testing.T) {
	for in, want := range map[string]string{
		"=cmd|' /C calc'!A0": "'=cmd|' /C calc'!A0",
		"+1":                 "'+1",
		"-1":                 "'-1",
		"@SUM(A1)":           "'@SUM(A1)",
		"example.com":        "example.com",
		"":                   "",
	} {
		if got := csvCell(in); got != want {
			t.Errorf("csvCell(%q) = %q, want %q", in, got, want)
		}
	}
}

// A form from before the connection log (a page left open across the upgrade, a script) has
// none of its fields: saving it must not shorten the retention or widen what is stored.
func TestConnLogSettingsSurviveAFormWithoutThem(t *testing.T) {
	connLogPanel(t)
	s := &SettingService{}
	all, err := s.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	want := ConnLogSettings{Enable: true, RetentionDays: 30, MaxSizeMB: 2048, Destination: "full", Source: "full"}
	all.ConnLogEnable = true
	if err := s.UpdateAllSetting(all, SecretClears{}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetConnLogSettings(); err != nil || got != want {
		t.Fatalf("a form with only the switch: %+v (err %v), want the defaults %+v", got, err, want)
	}

	all.ConnLogRetentionDays, all.ConnLogMaxSizeMB, all.ConnLogDestination, all.ConnLogSource = 365, 4096, "domain", "masked"
	if err := s.UpdateAllSetting(all, SecretClears{}); err != nil {
		t.Fatal(err)
	}
	all.ConnLogRetentionDays, all.ConnLogMaxSizeMB, all.ConnLogDestination, all.ConnLogSource = 0, 0, "", ""
	if err := s.UpdateAllSetting(all, SecretClears{}); err != nil {
		t.Fatal(err)
	}
	want = ConnLogSettings{Enable: true, RetentionDays: 365, MaxSizeMB: 4096, Destination: "domain", Source: "masked"}
	if got, err := s.GetConnLogSettings(); err != nil || got != want {
		t.Errorf("settings after an old form: %+v (err %v), want what was stored %+v", got, err, want)
	}
}

// A database that cannot be read says nothing about the settings. Taking that for "off"
// would drop the read position, and for the default retention would delete stored days.
func TestConnLogTickWaitsForSettingsItCannotRead(t *testing.T) {
	accessLog := connLogPanel(t)
	setConnLog(t, true, connlog.DestinationFull, connlog.SourceFull)
	svc := &ConnLogService{}
	appendAccessLog(t, accessLog, "")
	svc.Tick()

	if err := database.CloseDB(); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Format("2006/01/02 15:04:05.000000")
	appendAccessLog(t, accessLog, stamp+" from 203.0.113.77:1234 accepted tcp:during.test:443 [in-1 >> direct] email: alice\n")
	svc.Tick()
	if err := database.InitDB(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	svc.Tick()

	page, err := svc.Query(context.Background(), connlog.ConnFilter{})
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Host != "during.test" {
		t.Errorf("after the database came back: %+v, err %v; want the line logged meanwhile", page, err)
	}
}

// The access log the panel turned on for itself holds whole lines, whatever the storage
// policy says; once the connection log is off nothing reads it and it must not stay.
func TestConnLogOffEmptiesTheAccessLogItTurnedOn(t *testing.T) {
	accessLog := connLogPanel(t)
	ours := filepath.Join(config.GetLogFolder(), connLogAccessLogName)
	line := "2024/01/02 15:04:05.000000 from 1.2.3.4:5 accepted tcp:example.com:443 [in-1 >> direct] email: alice\n"
	appendAccessLog(t, ours, line)
	appendAccessLog(t, accessLog, line)
	(&ConnLogService{}).Tick()
	if info, err := os.Stat(ours); err != nil || info.Size() != 0 {
		t.Errorf("the panel's own access log after a step with the log off: %v, err %v; want it empty", info, err)
	}
	if info, err := os.Stat(accessLog); err != nil || info.Size() == 0 {
		t.Errorf("the operator's access log was touched: %v, err %v", info, err)
	}
}
