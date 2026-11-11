package service

import (
	"context"
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/connlog"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// ConnLogExportMaxRows bounds one CSV export; a longer answer needs a narrower filter.
const ConnLogExportMaxRows = 100000

const connLogStepTimeout = 2 * time.Minute

// ConnLogService runs the connection log (internal/connlog): it collects while the setting
// is on, prunes whenever data exists, and answers the panel's queries.
type ConnLogService struct {
	settingService SettingService
}

var (
	connLogCollector = &connlog.Collector{}

	// The access log's path sits in the generated Xray config, which is large and re-read
	// here every few seconds: it is parsed again only after the file changes.
	connLogPathMu      sync.Mutex
	connLogPathStamp   os.FileInfo
	connLogPath        string
	connLogLastFailure string
	// The policy of the last settings read that worked, for a truncation during one that does not.
	connLogPolicy connlog.Policy
)

func connLogAccessLogPath() string {
	connLogPathMu.Lock()
	defer connLogPathMu.Unlock()
	info, err := os.Stat(xray.GetConfigPath())
	if err != nil {
		return ""
	}
	if connLogPathStamp == nil || !info.ModTime().Equal(connLogPathStamp.ModTime()) || info.Size() != connLogPathStamp.Size() {
		path, err := xray.GetAccessLogPath()
		if err != nil || path == "none" {
			path = ""
		}
		connLogPath, connLogPathStamp = path, info
	}
	return connLogPath
}

func (s *ConnLogService) config() (connlog.Config, bool, error) {
	settings, err := s.settingService.GetConnLogSettings()
	if err != nil {
		return connlog.Config{}, false, err
	}
	policy := connlog.Policy{Destination: settings.Destination, Source: settings.Source}
	connLogPathMu.Lock()
	connLogPolicy = policy
	connLogPathMu.Unlock()
	return connlog.Config{
		Dir:           config.GetConnLogFolderPath(),
		Open:          func(path string) (*os.File, error) { return xray.OpenLogFile(path, os.O_RDONLY) },
		Policy:        policy,
		RetentionDays: settings.RetentionDays,
		MaxBytes:      int64(settings.MaxSizeMB) << 20,
	}, settings.Enable, nil
}

// emptyOwnAccessLog cuts the access log the panel turned on for itself: it holds whole
// lines, whatever the storage policy, and nothing reads it once the connection log is off.
func emptyOwnAccessLog() {
	path := filepath.Join(xrayLogFolder(), connLogAccessLogName)
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return
	}
	if f, err := xray.OpenLogFile(path, os.O_WRONLY); err == nil {
		_ = f.Truncate(0)
		_ = f.Close()
	}
}

// Tick is the cron step: read the access log's new lines while the log is on, stop
// reading while it is off, and delete what the retention and the size cap no longer allow.
func (s *ConnLogService) Tick() {
	// Settings that cannot be read are neither "off" nor the defaults: the step waits for them.
	cfg, enabled, err := s.config()
	ctx, cancel := context.WithTimeout(context.Background(), connLogStepTimeout)
	defer cancel()
	switch {
	case err != nil:
	case enabled:
		cfg.LogPath = connLogAccessLogPath()
		err = connLogCollector.Step(ctx, cfg, time.Now())
	default:
		err = connLogCollector.Halt(ctx, cfg, time.Now())
		emptyOwnAccessLog()
	}
	// A full disk fails every step the same way; one line per distinct failure is enough.
	failure := ""
	if err != nil {
		failure = err.Error()
	}
	connLogPathMu.Lock()
	changed := failure != connLogLastFailure
	connLogLastFailure = failure
	connLogPathMu.Unlock()
	if changed && failure != "" {
		logger.Warning("connection log:", failure)
	}
}

// TruncateAccessLog empties the access log at path through truncate. With the connection
// log on, the lines not read yet are stored first.
func TruncateAccessLog(path string, truncate func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), connLogStepTimeout)
	defer cancel()
	_, _, _ = (&ConnLogService{}).config()
	connLogPathMu.Lock()
	policy := connLogPolicy
	connLogPathMu.Unlock()
	return connLogCollector.TruncateLog(ctx, path, policy, truncate)
}

// Query returns one page of the log, with whether it is being recorded and its size on disk.
func (s *ConnLogService) Query(ctx context.Context, filter connlog.ConnFilter) (*connlog.ConnPage, error) {
	dir := config.GetConnLogFolderPath()
	page, err := connlog.Query(ctx, dir, filter)
	if err != nil {
		return nil, err
	}
	page.Enabled = s.settingService.GetConnLogEnable()
	page.SizeBytes = connlog.Usage(dir)
	return page, nil
}

// csvCell keeps a spreadsheet from running a value as a formula: the host is whatever
// text a client asked Xray to connect to.
func csvCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

// Export writes the connections matching the filter as CSV, newest first, at most
// ConnLogExportMaxRows of them. Times are UTC.
func (s *ConnLogService) Export(ctx context.Context, w io.Writer, filter connlog.ConnFilter) error {
	filter.Before = ""
	out := csv.NewWriter(w)
	if err := out.Write([]string{"time_utc", "client", "inbound", "outbound", "source", "source_port", "network", "host", "port"}); err != nil {
		return err
	}
	err := connlog.Scan(ctx, config.GetConnLogFolderPath(), filter, ConnLogExportMaxRows, func(rec connlog.ConnRecord, _ string) error {
		return out.Write([]string{
			time.UnixMilli(rec.Time).UTC().Format("2006-01-02T15:04:05.000Z"),
			csvCell(rec.Client), csvCell(rec.Inbound), csvCell(rec.Outbound),
			csvCell(rec.Source), strconv.Itoa(rec.SourcePort), rec.Network,
			csvCell(rec.Host), strconv.Itoa(rec.Port),
		})
	})
	out.Flush()
	if err != nil {
		return err
	}
	return out.Error()
}
