package panelmigrate

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// legacySchema is the shape of a 3x-ui 2.x database: clients live only in
// inbounds.settings.
const legacySchema = `
CREATE TABLE users (id integer primary key autoincrement, username text, password text);
CREATE TABLE inbounds (id integer primary key autoincrement, user_id integer, up integer, down integer,
  total integer, remark text, enable numeric, expiry_time integer, listen text, port integer,
  protocol text, settings text, stream_settings text, tag text, sniffing text);
CREATE TABLE settings (id integer primary key autoincrement, key text, value text);
CREATE TABLE client_traffics (id integer primary key autoincrement, inbound_id integer, enable numeric,
  email text, up integer, down integer, expiry_time integer, total integer, reset integer);
`

// clientsTableSchema adds the table 3x-ui 3.x keeps next to the JSON.
const clientsTableSchema = `
CREATE TABLE clients (id integer primary key autoincrement, email text, sub_id text, uuid text, enable numeric);
`

func newDB(t *testing.T, schema string, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x-ui.db")
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range append([]string{schema}, stmts...) {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return path
}

func fileSum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

var fixture = []string{
	`INSERT INTO users (username, password) VALUES ('admin', '$2a$10$abcdefghijklmnopqrstuv')`,
	`INSERT INTO inbounds (id, port, protocol, enable, settings) VALUES
	 (1, 443, 'vless', 1, '{"clients":[{"email":"A@x","subId":"s1","enable":true},{"email":"b@x","subId":"s2","enable":false}]}'),
	 (2, 8443, 'vmess', 0, '{"clients":[{"email":"c@x","subId":"s3"}]}'),
	 (3, 9443, 'trojan', 1, '{"clients":[{"email":"a@x","subId":"s1"},{"email":"d@x","subId":"s4"},{"email":"e@x","subId":"s5"},{"email":"f@x","subId":"s6"}]}'),
	 (4, 1080, 'mixed', 1, '{"auth":"noauth"}'),
	 (5, 1081, 'socks', 1, 'not json')`,
	`INSERT INTO settings (key, value) VALUES ('webPort','54321'),('webBasePath','/secret/'),('subPort','2096'),
	 ('subPath','/sub/'),('tgBotToken','123:never-print'),('twoFactorEnable','true'),('twoFactorToken','SEED')`,
	`INSERT INTO client_traffics (inbound_id, email) VALUES (1,'A@x'),(1,'b@x'),(2,'c@x')`,
}

func TestInspectLegacySchema(t *testing.T) {
	path := newDB(t, legacySchema, fixture...)
	before := fileSum(t, path)

	r, err := inspectSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Inbounds != 5 || len(r.InboundList) != 5 {
		t.Fatalf("inbounds = %d (%d listed), want 5", r.Inbounds, len(r.InboundList))
	}
	if got := r.InboundList[1]; got.ID != 2 || got.Port != 8443 || got.Enable {
		t.Fatalf("inbound 2 = %+v", got)
	}
	if r.ClientEntries != 7 {
		t.Fatalf("client entries = %d, want 7", r.ClientEntries)
	}
	// A@x and a@x are one client (emails are case-insensitive in the panel).
	if r.Clients != 6 {
		t.Fatalf("clients = %d, want 6", r.Clients)
	}
	if r.ClientsTable != -1 {
		t.Fatalf("clientsTable = %d, want -1 for a schema without the table", r.ClientsTable)
	}
	if r.ClientTraffics != 3 {
		t.Fatalf("client traffics = %d, want 3", r.ClientTraffics)
	}
	// s2 is a disabled client, s3 sits on a disabled inbound. The sample
	// takes the first client of every inbound before a second one (s1 of
	// inbound 3 is already in) and stops at maxSampleSubIDs.
	if got := strings.Join(r.SampleSubIDs, ","); got != "s1,s4,s5,s6" {
		t.Fatalf("sample subIds = %q, want s1,s4,s5,s6", got)
	}
	if len(r.Logins) != 1 || r.Logins[0].Username != "admin" || !r.Logins[0].Bcrypt || len(r.Logins[0].PasswordSHA256) != 64 {
		t.Fatalf("logins = %+v", r.Logins)
	}
	if r.Settings["webPort"] != "54321" || r.Settings["webBasePath"] != "/secret/" || r.Settings["twoFactorEnable"] != "true" {
		t.Fatalf("settings = %v", r.Settings)
	}
	if _, leaked := r.Settings["tgBotToken"]; leaked {
		t.Fatal("inspect reported the Telegram bot token")
	}
	if r.TwoFactorSHA256 == "" || r.TwoFactorSHA256 == "SEED" {
		t.Fatalf("2FA token must be reported hashed, got %q", r.TwoFactorSHA256)
	}
	if fileSum(t, path) != before {
		t.Fatal("inspect changed the database file")
	}
}

func TestInspectClientsTable(t *testing.T) {
	path := newDB(t, legacySchema+clientsTableSchema, append(fixture,
		`INSERT INTO clients (email, sub_id, enable) VALUES ('a@x','s1',1),('b@x','s2',0),('only-table@x','t1',1)`)...)
	r, err := inspectSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.ClientsTable != 3 {
		t.Fatalf("clientsTable = %d, want 3", r.ClientsTable)
	}
	if r.Clients != 7 {
		t.Fatalf("clients = %d, want 7 (JSON and table merged by email)", r.Clients)
	}
}

func TestInspectRejectsForeignDatabase(t *testing.T) {
	path := newDB(t, `CREATE TABLE something (id integer);`)
	if _, err := inspectSQLite(path); err == nil || !strings.Contains(err.Error(), "not a 3x-ui panel database") {
		t.Fatalf("err = %v, want a not-a-panel-database error", err)
	}
}

func TestSampleSubIDsCoversInboundsFirst(t *testing.T) {
	got := sampleSubIDs([][]string{{"a1", "a2", "a3"}, nil, {"b1"}, {"c1", "c2"}}, 4)
	if strings.Join(got, ",") != "a1,b1,c1,a2" {
		t.Fatalf("sample = %v, want a1,b1,c1,a2", got)
	}
	if got := sampleSubIDs(nil, 5); got == nil || len(got) != 0 {
		t.Fatalf("empty sample = %#v, want an empty non-nil slice", got)
	}
}

func TestOpenReadOnlyDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := OpenSQLiteReadOnly(path); err == nil {
		t.Fatal("opening a missing database succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only open created %s", path)
	}
}

func TestBackupSQLiteCopiesUncheckpointedWAL(t *testing.T) {
	path := newDB(t, legacySchema)
	// A running panel: a writer with committed rows still in the WAL.
	live, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if _, err := live.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	for _, s := range fixture {
		if _, err := live.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if st, err := os.Stat(path + "-wal"); err != nil || st.Size() == 0 {
		t.Fatalf("expected a non-empty WAL, stat err=%v", err)
	}

	dst := filepath.Join(t.TempDir(), "backup.db")
	if err := BackupSQLite(path, dst); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, want 0600", st.Mode().Perm())
	}
	// The copy is self-contained: no WAL next to it, and it holds the rows.
	if _, err := os.Stat(dst + "-wal"); err == nil {
		t.Fatal("backup left a WAL file behind")
	}
	r, err := inspectSQLite(dst)
	if err != nil {
		t.Fatal(err)
	}
	if r.Inbounds != 5 || r.Clients != 6 {
		t.Fatalf("backup holds %d inbounds / %d clients, want 5 / 6", r.Inbounds, r.Clients)
	}

	if err := BackupSQLite(path, dst); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second backup onto the same file: err = %v", err)
	}
}

func TestCheckSQLiteRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(path, []byte("this is not a database, just bytes that are long enough to matter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckSQLite(path); err == nil {
		t.Fatal("integrity check passed on a corrupt file")
	}
}

func TestRunCLI(t *testing.T) {
	path := newDB(t, legacySchema, fixture...)
	var out, errOut bytes.Buffer
	if code := RunCLI([]string{"inspect", "-db", path}, &out, &errOut); code != 0 {
		t.Fatalf("inspect exit %d: %s", code, errOut.String())
	}
	var r Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("inspect output is not JSON: %v\n%s", err, out.String())
	}
	if r.Inbounds != 5 || r.Dialect != "sqlite" {
		t.Fatalf("report = %+v", r)
	}
	if strings.Contains(out.String(), "never-print") || strings.Contains(out.String(), "SEED") {
		t.Fatal("inspect output contains a secret")
	}

	dst := filepath.Join(t.TempDir(), "b.db")
	if code := RunCLI([]string{"backup", "-src", path, "-dst", dst}, io.Discard, &errOut); code != 0 {
		t.Fatalf("backup exit %d: %s", code, errOut.String())
	}

	for _, args := range [][]string{nil, {"inspect"}, {"inspect", "-db", path, "-postgres"}, {"backup", "-src", path}, {"bogus"}} {
		if code := RunCLI(args, io.Discard, io.Discard); code != 2 {
			t.Fatalf("RunCLI(%q) = %d, want usage error 2", args, code)
		}
	}
	t.Setenv("XUI_DB_DSN", "")
	if code := RunCLI([]string{"inspect", "-postgres"}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("inspect -postgres with an empty DSN = %d, want 1", code)
	}
}
