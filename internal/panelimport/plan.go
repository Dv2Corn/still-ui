// Package panelimport reads another panel's database, without changing it, into a Plan of
// still-ui inbounds, clients and hosts that service.PanelImportService checks and writes.
package panelimport

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/panelmigrate"
)

const (
	SourceMarzban = "marzban"
	SourceXUI     = "x-ui"
)

// Plan is what a source panel holds, already in still-ui's shape.
type Plan struct {
	Source   string
	Inbounds []*model.Inbound
	// Hosts are keyed by the tag of the plan inbound they belong to.
	Hosts map[string][]model.Host
	// SubSecret and SubUsers let the subscription server accept the source's own links.
	SubSecret string
	SubUsers  []model.LegacySubUser
	// Notes say what could not be carried over and why.
	Notes  []string
	Admins []string
}

func (p *Plan) note(format string, args ...any) {
	p.Notes = append(p.Notes, fmt.Sprintf(format, args...))
}

// Read loads a plan from a source database; xrayConfig is Marzban's xray_config.json.
func Read(source, dbPath string, xrayConfig []byte) (*Plan, error) {
	switch source {
	case SourceMarzban:
		return ReadMarzban(dbPath, xrayConfig)
	case SourceXUI:
		return ReadXUI(dbPath)
	}
	return nil, fmt.Errorf("unknown source %q: use %s or %s", source, SourceMarzban, SourceXUI)
}

// ResolveMarzbanFiles accepts Marzban's data folder or its database file and finds the
// xray_config.json beside it unless one is named.
func ResolveMarzbanFiles(path, configPath string) (string, string) {
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		path = filepath.Join(path, "db.sqlite3")
	}
	if configPath == "" {
		configPath = filepath.Join(filepath.Dir(path), "xray_config.json")
	}
	return path, configPath
}

func openSource(path string) (*gorm.DB, func(), error) {
	gdb, err := panelmigrate.OpenSQLiteReadOnly(path)
	if err != nil {
		return nil, nil, err
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, nil, err
	}
	return gdb, func() { _ = sqlDB.Close() }, nil
}

func hasTable(gdb *gorm.DB, name string) bool {
	var n int64
	gdb.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n)
	return n > 0
}

// rows reads a whole table as maps, so a source release without a newer column still loads.
func rows(gdb *gorm.DB, table string) ([]map[string]any, error) {
	var out []map[string]any
	if !hasTable(gdb, table) {
		return nil, nil
	}
	if err := gdb.Raw("SELECT * FROM " + table + " ORDER BY rowid").Scan(&out).Error; err != nil {
		return nil, fmt.Errorf("read %s: %w", table, err)
	}
	// A column of a type gorm does not know (Marzban's JSON) arrives as a pointer.
	for _, row := range out {
		for key, value := range row {
			if p, ok := value.(*any); ok {
				row[key] = *p
			}
		}
	}
	return out, nil
}

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	}
	return fmt.Sprint(v)
}

func asInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case bool:
		if t {
			return 1
		}
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	case []byte:
		return asInt(string(t))
	}
	return 0
}

// asTime reads a naive UTC datetime column; the zero time means NULL or unreadable.
func asTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case string:
		for _, layout := range []string{"2006-01-02 15:04:05.999999", "2006-01-02T15:04:05.999999", time.RFC3339Nano} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed
			}
		}
	case []byte:
		return asTime(string(t))
	}
	return time.Time{}
}
