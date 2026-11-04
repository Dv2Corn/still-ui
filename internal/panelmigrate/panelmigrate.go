// Package panelmigrate backs the "migrate" command of migrate.sh: moving an
// existing 3x-ui installation over to still-ui and back.
//
// The shell script cannot rely on the sqlite3 CLI being installed, and it must
// read the 3x-ui database without changing it (no AutoMigrate, no seeders), so
// the two database operations it needs live here and run from the still-ui
// binary that is about to be installed:
//
//   - Inspect reads a panel database read-only and reports what a migration
//     has to keep: inbound and client counts, the panel login, the web and
//     subscription listener settings and a few subscription IDs to sample.
//   - BackupSQLite writes a transactionally consistent copy of a (possibly
//     live) SQLite database with VACUUM INTO and verifies it.
//
// Inspect works on every schema the 3x-ui releases still-ui can migrate from
// use: clients only inside inbounds.settings (2.x) and the clients table that
// 3.x keeps next to it.
package panelmigrate

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SettingKeys are the settings rows Inspect reports. They decide where the
// panel and the subscription server listen, which a migration has to keep and
// which the migration script needs to probe both before and after the switch.
// Secrets (bot tokens, the 2FA seed) are deliberately not among them.
var SettingKeys = []string{
	"webListen", "webDomain", "webPort", "webCertFile", "webKeyFile", "webBasePath",
	"subEnable", "subListen", "subPort", "subPath", "subDomain", "subCertFile", "subKeyFile",
	"subEncrypt", "subURI", "subJsonEnable", "subJsonPath",
	"twoFactorEnable",
}

// maxSampleSubIDs bounds how many subscriptions the script compares.
const maxSampleSubIDs = 5

// InboundBrief identifies one inbound. A migration must keep every inbound
// with its id and port: client links and firewall rules depend on both.
type InboundBrief struct {
	ID       int    `json:"id"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Enable   bool   `json:"enable"`
}

// Login identifies a panel account without revealing its password hash.
type Login struct {
	Username       string `json:"username"`
	PasswordSHA256 string `json:"passwordSha256"`
	// Bcrypt is false for the plain-text passwords of very old releases,
	// which the panel hashes on its first start (the login still works, but
	// the stored value changes).
	Bcrypt bool `json:"bcrypt"`
}

// Report is what Inspect found in a panel database.
type Report struct {
	Dialect         string            `json:"dialect"`
	Inbounds        int               `json:"inbounds"`
	InboundList     []InboundBrief    `json:"inboundList"`
	ClientEntries   int               `json:"clientEntries"`
	Clients         int               `json:"clients"`
	ClientEmails    []string          `json:"clientEmails"`
	ClientsTable    int               `json:"clientsTable"` // -1 when the table does not exist (2.x)
	ClientTraffics  int               `json:"clientTraffics"`
	Logins          []Login           `json:"logins"`
	TwoFactorSHA256 string            `json:"twoFactorTokenSha256,omitempty"`
	Settings        map[string]string `json:"settings"`
	SampleSubIDs    []string          `json:"sampleSubIds"`
}

// OpenSQLiteReadOnly opens a SQLite file without the right to change it. The
// file must exist: a read-only open must never create an empty database where
// the script expected one.
func OpenSQLiteReadOnly(path string) (*gorm.DB, error) {
	if st, err := os.Stat(path); err != nil {
		return nil, err
	} else if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + abs + "?mode=ro&_busy_timeout=10000"
	return gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
}

// OpenPostgres opens a PostgreSQL database by DSN.
func OpenPostgres(dsn string) (*gorm.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("empty PostgreSQL DSN")
	}
	return gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Inspect reads db and reports what a migration has to preserve. It only
// issues SELECTs.
func Inspect(gdb *gorm.DB) (*Report, error) {
	r := &Report{Dialect: gdb.Dialector.Name(), ClientsTable: -1, Settings: map[string]string{}}
	mig := gdb.Migrator()
	for _, table := range []string{"inbounds", "users", "settings"} {
		if !mig.HasTable(table) {
			return nil, fmt.Errorf("table %q not found: this is not a 3x-ui panel database", table)
		}
	}

	type inboundRow struct {
		ID       int
		Port     int
		Protocol string
		Enable   bool
		Settings sql.NullString
	}
	var inbounds []inboundRow
	if err := gdb.Raw("SELECT id, port, protocol, enable, settings FROM inbounds ORDER BY id").Scan(&inbounds).Error; err != nil {
		return nil, fmt.Errorf("read inbounds: %w", err)
	}

	emails := map[string]struct{}{}
	// Subscription IDs per inbound, so the sample can cover as many inbounds
	// (and so protocols) as possible.
	var perInbound [][]string
	for _, in := range inbounds {
		r.Inbounds++
		r.InboundList = append(r.InboundList, InboundBrief{ID: in.ID, Port: in.Port, Protocol: in.Protocol, Enable: in.Enable})
		if !in.Settings.Valid || strings.TrimSpace(in.Settings.String) == "" {
			continue
		}
		var settings struct {
			Clients []map[string]any `json:"clients"`
		}
		if err := json.Unmarshal([]byte(in.Settings.String), &settings); err != nil {
			// A malformed settings blob is the panel's problem, not ours; it
			// is counted the same way before and after the move.
			continue
		}
		var subIDs []string
		for _, c := range settings.Clients {
			r.ClientEntries++
			if email, _ := c["email"].(string); strings.TrimSpace(email) != "" {
				emails[strings.ToLower(strings.TrimSpace(email))] = struct{}{}
			}
			subID, _ := c["subId"].(string)
			enabled := true
			if v, ok := c["enable"].(bool); ok {
				enabled = v
			}
			if subID != "" && enabled && in.Enable {
				subIDs = append(subIDs, subID)
			}
		}
		perInbound = append(perInbound, subIDs)
	}

	if mig.HasTable("clients") {
		var tableEmails []string
		if err := gdb.Raw("SELECT email FROM clients").Scan(&tableEmails).Error; err != nil {
			return nil, fmt.Errorf("read clients: %w", err)
		}
		r.ClientsTable = len(tableEmails)
		for _, e := range tableEmails {
			if strings.TrimSpace(e) != "" {
				emails[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
			}
		}
	}
	r.Clients = len(emails)
	r.ClientEmails = make([]string, 0, len(emails))
	for e := range emails {
		r.ClientEmails = append(r.ClientEmails, e)
	}
	sort.Strings(r.ClientEmails)

	if mig.HasTable("client_traffics") {
		var n int64
		if err := gdb.Raw("SELECT COUNT(*) FROM client_traffics").Scan(&n).Error; err != nil {
			return nil, fmt.Errorf("read client_traffics: %w", err)
		}
		r.ClientTraffics = int(n)
	}

	// The sample comes from inbounds.settings on every schema: it is what the
	// subscription server of both 2.x and 3.x resolves a subId against.
	r.SampleSubIDs = sampleSubIDs(perInbound, maxSampleSubIDs)

	type userRow struct {
		Username string
		Password string
	}
	var users []userRow
	if err := gdb.Raw("SELECT username, password FROM users ORDER BY id").Scan(&users).Error; err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}
	for _, u := range users {
		r.Logins = append(r.Logins, Login{
			Username:       u.Username,
			PasswordSHA256: hashHex(u.Password),
			Bcrypt:         strings.HasPrefix(u.Password, "$2"),
		})
	}
	if r.Logins == nil {
		r.Logins = []Login{}
	}

	type settingRow struct {
		Key   string
		Value string
	}
	var settings []settingRow
	if err := gdb.Raw("SELECT key, value FROM settings").Scan(&settings).Error; err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	wanted := map[string]bool{}
	for _, k := range SettingKeys {
		wanted[k] = true
	}
	for _, s := range settings {
		switch {
		case wanted[s.Key]:
			r.Settings[s.Key] = s.Value
		case s.Key == "twoFactorToken" && s.Value != "":
			r.TwoFactorSHA256 = hashHex(s.Value)
		}
	}
	return r, nil
}

// sampleSubIDs picks up to limit distinct IDs round-robin over the inbounds:
// the first eligible client of each inbound before any second one.
func sampleSubIDs(perInbound [][]string, limit int) []string {
	out := []string{}
	seen := map[string]bool{}
	for round := 0; len(out) < limit; round++ {
		more := false
		for _, ids := range perInbound {
			if round >= len(ids) {
				continue
			}
			more = true
			if id := ids[round]; !seen[id] && len(out) < limit {
				seen[id] = true
				out = append(out, id)
			}
		}
		if !more {
			break
		}
	}
	return out
}

// BackupSQLite writes a consistent copy of the SQLite database src to dst
// (which must not exist) and checks it with PRAGMA integrity_check. src may be
// in use by a running panel: VACUUM INTO reads it inside one transaction, so
// the copy never mixes two states, unlike a plain cp of the file and its WAL.
func BackupSQLite(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	} else if !os.IsNotExist(err) {
		return err
	}
	absDst, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	gdb, err := OpenSQLiteReadOnly(src)
	if err != nil {
		return err
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	if _, err := sqlDB.Exec("VACUUM INTO ?", absDst); err != nil {
		_ = os.Remove(absDst)
		return fmt.Errorf("VACUUM INTO: %w", err)
	}
	if err := os.Chmod(absDst, 0o600); err != nil {
		return err
	}
	return CheckSQLite(absDst)
}

// CheckSQLite runs PRAGMA integrity_check on a SQLite file.
func CheckSQLite(path string) error {
	gdb, err := OpenSQLiteReadOnly(path)
	if err != nil {
		return err
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	var result string
	if err := sqlDB.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check: %s", result)
	}
	return nil
}
