package panelimport

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// xuiSchema is the inbounds table of vaxilu/x-ui 0.3.2 plus the client_traffics table
// 3x-ui 2.x added; clients live only in inbounds.settings in both.
const xuiSchema = `
CREATE TABLE users (id integer PRIMARY KEY AUTOINCREMENT, username text, password text);
CREATE TABLE inbounds (id integer PRIMARY KEY AUTOINCREMENT, user_id integer, up integer, down integer, total integer,
  remark text, enable numeric, expiry_time integer, listen text, port integer UNIQUE, protocol text, settings text,
  stream_settings text, tag text UNIQUE, sniffing text);
CREATE TABLE client_traffics (id integer PRIMARY KEY AUTOINCREMENT, inbound_id integer, enable numeric, email text UNIQUE,
  up integer, down integer, expiry_time integer, total integer);
`

func buildXUI(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x-ui.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		xuiSchema + extra,
		`INSERT INTO users (username, password) VALUES ('admin', 'admin')`,
		`INSERT INTO inbounds (user_id, up, down, total, remark, enable, expiry_time, listen, port, protocol, settings, stream_settings, tag, sniffing) VALUES
		 (1, 10, 20, 0, 'old vmess', 1, 0, '', 31001, 'vmess', '{"clients":[{"id":"11111111-1111-4111-8111-111111111111","alterId":0}],"disableInsecureEncryption":false}', '{"network":"tcp","security":"none"}', 'inbound-31001', '{}'),
		 (1, 0, 0, 0, 'vless', 0, 0, '', 31002, 'vless', '{"clients":[{"id":"22222222-2222-4222-8222-222222222222","email":"kate","flow":"xtls-rprx-direct","tgId":"12 34","totalGB":1000,"expiryTime":0,"enable":true}],"decryption":"none"}', '{"network":"tcp","security":"none"}', 'inbound-31002', '{}')`,
		`INSERT INTO client_traffics (inbound_id, enable, email, up, down, expiry_time, total) VALUES (2, 1, 'kate', 300, 400, 0, 1000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path
}

func TestReadXUI(t *testing.T) {
	plan, err := ReadXUI(buildXUI(t, ""))
	if err != nil {
		t.Fatalf("ReadXUI: %v", err)
	}
	if len(plan.Inbounds) != 2 || !slices.Equal(plan.Admins, []string{"admin"}) {
		t.Fatalf("%d inbounds, admins %q", len(plan.Inbounds), plan.Admins)
	}
	old, vless := plan.Inbounds[0], plan.Inbounds[1]
	if old.Port != 31001 || old.Protocol != "vmess" || old.Tag != "inbound-31001" || !old.Enable || old.Up != 10 || old.Down != 20 || old.Remark != "old vmess" {
		t.Errorf("vmess inbound = %+v", old)
	}
	if vless.Enable {
		t.Errorf("a disabled inbound came over enabled")
	}
	var settings struct {
		Clients []map[string]any `json:"clients"`
	}
	if err := json.Unmarshal([]byte(old.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Clients[0]["email"] != "inbound-31001-1" || settings.Clients[0]["id"] != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("nameless client = %v, want the generated email and its own id", settings.Clients[0])
	}
	if !slices.ContainsFunc(plan.Notes, func(n string) bool { return strings.Contains(n, `named "inbound-31001-1"`) }) {
		t.Errorf("no note about the generated email: %q", plan.Notes)
	}
	if err := json.Unmarshal([]byte(vless.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	kate := settings.Clients[0]
	if kate["flow"] != "" || kate["tgId"] != float64(1234) {
		t.Errorf("kate = %v, want the removed direct flow cleared and tgId as the number 1234", kate)
	}
	want := xray.ClientTraffic{Email: "kate", Enable: true, Up: 300, Down: 400, Total: 1000}
	if len(vless.ClientStats) != 1 || vless.ClientStats[0] != want {
		t.Errorf("kate's traffic = %+v, want %+v", vless.ClientStats, want)
	}
}

func TestReadXUIRefusesV3(t *testing.T) {
	_, err := ReadXUI(buildXUI(t, `CREATE TABLE clients (id integer PRIMARY KEY, email text);`))
	if err == nil || !strings.HasPrefix(err.Error(), "this is a 3x-ui v3 or still-ui database") {
		t.Fatalf("a database with a clients table: %v", err)
	}
}
