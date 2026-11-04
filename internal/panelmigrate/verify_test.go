package panelmigrate

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

func reportOf(t *testing.T, path string) *Report {
	t.Helper()
	r, err := inspectSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mutate(t *testing.T, path, stmt string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyIdenticalCopyPasses(t *testing.T) {
	path := newDB(t, legacySchema, fixture...)
	lines, ok := Verify(reportOf(t, path), reportOf(t, path))
	if !ok {
		t.Fatalf("identical databases failed verification:\n%s", strings.Join(lines, "\n"))
	}
}

func TestVerifyCatchesLoss(t *testing.T) {
	cases := map[string]string{
		"inbound removed":   `DELETE FROM inbounds WHERE id = 5`,
		"port changed":      `UPDATE inbounds SET port = 1 WHERE id = 1`,
		"inbound disabled":  `UPDATE inbounds SET enable = 0 WHERE id = 1`,
		"protocol changed":  `UPDATE inbounds SET protocol = 'vmess' WHERE id = 1`,
		"client removed":    `UPDATE inbounds SET settings = '{"clients":[]}' WHERE id = 2`,
		"password changed":  `UPDATE users SET password = '$2a$10$zzzzzzzzzzzzzzzzzzzzzz'`,
		"username changed":  `UPDATE users SET username = 'root'`,
		"2FA seed changed":  `UPDATE settings SET value = 'OTHER' WHERE key = 'twoFactorToken'`,
		"panel port moved":  `UPDATE settings SET value = '2053' WHERE key = 'webPort'`,
		"sub path reseeded": `UPDATE settings SET value = '/random/' WHERE key = 'subPath'`,
		"base path dropped": `DELETE FROM settings WHERE key = 'webBasePath'`,
		"cert added":        `INSERT INTO settings (key, value) VALUES ('subCertFile', '/x.pem')`,
	}
	for name, stmt := range cases {
		t.Run(name, func(t *testing.T) {
			path := newDB(t, legacySchema, fixture...)
			pre := reportOf(t, path)
			mutate(t, path, stmt)
			if lines, ok := Verify(pre, reportOf(t, path)); ok {
				t.Fatalf("verification passed after %q:\n%s", stmt, strings.Join(lines, "\n"))
			}
		})
	}
}

func TestVerifyToleratesKnownMigrations(t *testing.T) {
	path := newDB(t, legacySchema, append(fixture, `UPDATE users SET password = 'plain-text'`)...)
	pre := reportOf(t, path)
	// still-ui hashes a plain-text password of a very old release and renames
	// SOCKS inbounds to "mixed"; neither is a loss.
	mutate(t, path, `UPDATE users SET password = '$2a$10$hashedhashedhashedhashe'`)
	mutate(t, path, `UPDATE inbounds SET protocol = 'mixed' WHERE id = 5`)
	if lines, ok := Verify(pre, reportOf(t, path)); !ok {
		t.Fatalf("known migrations failed verification:\n%s", strings.Join(lines, "\n"))
	}
}

func TestWriteEnv(t *testing.T) {
	path := newDB(t, legacySchema, append(fixture, `INSERT INTO settings (key, value) VALUES ('subDomain', 'a' || char(9) || 'b')`)...)
	var out bytes.Buffer
	if err := WriteEnv(&out, reportOf(t, path)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"dialect\tsqlite\n", "inbounds\t5\n", "clients\t6\n", "setting.webPort\t54321\n",
		"setting.webBasePath\t/secret/\n", "sampleSubId\ts1\n", "sampleSubId\ts5\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("env output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "subDomain") || strings.Contains(got, "tgBotToken") || strings.Contains(got, "twoFactorToken") {
		t.Fatalf("env output carries a value it must not:\n%s", got)
	}
}

func b64(s string) []byte { return []byte(base64.StdEncoding.EncodeToString([]byte(s))) }

func vmess(t *testing.T, ps string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"v": "2", "ps": ps, "add": "203.0.113.1", "port": 443, "id": "u"})
	if err != nil {
		t.Fatal(err)
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(raw)
}

func TestCompareSubscriptions(t *testing.T) {
	a := b64("vless://u@203.0.113.1:443?security=reality#main-📊10GB\n" + vmess(t, "vm-📊10GB") + "\n")
	remarks := b64("vless://u@203.0.113.1:443?security=reality#main-📊9GB\n" + vmess(t, "vm-📊9GB") + "\n")
	other := b64("vless://u@203.0.113.1:8443?security=reality#main-📊10GB\n" + vmess(t, "vm-📊10GB") + "\n")
	fewer := b64("vless://u@203.0.113.1:443?security=reality#main-📊10GB\n")
	cases := []struct {
		name string
		a, b []byte
		want int
	}{
		{"identical", a, a, SubIdentical},
		{"remark-only change", a, remarks, SubRemarksDiffer},
		{"changed port", a, other, SubDifferent},
		{"lost link", a, fewer, SubDifferent},
		{"error pages", []byte("not found"), []byte("Not Found"), SubDifferent},
		{"plain-text bodies, other remarks", []byte("trojan://p@203.0.113.1:443#a\n"), []byte("trojan://p@203.0.113.1:443#b\n"), SubRemarksDiffer},
	}
	for _, c := range cases {
		if got, note := CompareSubscriptions(c.a, c.b); got != c.want {
			t.Errorf("%s: = %d (%s), want %d", c.name, got, note, c.want)
		}
	}
}

// A 3x-ui 2.x link next to the one still-ui writes for the same client:
// spiderX drawn per request, two parameters added, host and keys unchanged.
func TestCompareSubscriptionsAcrossLinkFormats(t *testing.T) {
	old := "vless://id@sub.example:24431?flow=xtls-rprx-vision&fp=chrome&pbk=PBK&security=reality&sid=6ba8&sni=www.microsoft.com&spx=%2F1T30I2XR84DUc89&type=tcp#vl-1"
	newer := "vless://id@sub.example:24431?encryption=none&flow=xtls-rprx-vision&fp=chrome&pbk=PBK&security=reality&sid=6ba8&sni=www.microsoft.com&spx=%2F908b911d41445df&support-x25519mlkem768=true&type=tcp#vl-1"
	got, note := CompareSubscriptions(b64(old+"\n"), b64(newer+"\n"))
	if got != SubSameSettings || note != "added: encryption, support-x25519mlkem768" {
		t.Fatalf("= %d %q, want %d with the added parameters named", got, note, SubSameSettings)
	}

	for name, changed := range map[string]string{
		"short id":  strings.Replace(newer, "sid=6ba8", "sid=ffff", 1),
		"host":      strings.Replace(newer, "@sub.example:", "@other.example:", 1),
		"user":      strings.Replace(newer, "vless://id@", "vless://id2@", 1),
		"flow lost": strings.Replace(newer, "flow=xtls-rprx-vision&", "", 1),
		"scheme":    strings.Replace(newer, "vless://", "trojan://", 1),
	} {
		got, note := CompareSubscriptions(b64(old+"\n"), b64(changed+"\n"))
		if got != SubDifferent {
			t.Errorf("%s changed: = %d %q, want %d", name, got, note, SubDifferent)
		}
		if strings.Contains(note, "6ba8") || strings.Contains(note, "PBK") {
			t.Errorf("%s: the note leaks a value: %q", name, note)
		}
	}

	// Shadowsocks 2022: base64 userinfo (2.x) and percent-encoded plain text
	// (3.x) spell the same method and passwords.
	ssOld := "ss://" + base64.StdEncoding.EncodeToString([]byte("2022-blake3-aes-256-gcm:c2Vy+/=:Y2xp+/=")) + "@sub.example:24434?type=tcp#ss-1"
	ssNew := "ss://2022-blake3-aes-256-gcm:c2Vy%2B%2F%3D:Y2xp%2B%2F%3D@sub.example:24434?type=tcp#ss-1"
	if got, note := CompareSubscriptions(b64(ssOld+"\n"), b64(ssNew+"\n")); got != SubSameSettings {
		t.Fatalf("shadowsocks userinfo spellings = %d %q, want %d", got, note, SubSameSettings)
	}
	ssOther := "ss://2022-blake3-aes-256-gcm:c2Vy%2B%2F%3D:b3RoZXI%3D@sub.example:24434?type=tcp#ss-1"
	if got, _ := CompareSubscriptions(b64(ssOld+"\n"), b64(ssOther+"\n")); got != SubDifferent {
		t.Fatalf("another shadowsocks password = %d, want %d", got, SubDifferent)
	}

	vmOld := vmess(t, "vm")
	raw, _ := json.Marshal(map[string]any{"v": "2", "ps": "vm-new", "add": "203.0.113.1", "port": 443, "id": "u", "scy": "auto"})
	vmNew := "vmess://" + base64.StdEncoding.EncodeToString(raw)
	if got, note := CompareSubscriptions(b64(vmOld+"\n"), b64(vmNew+"\n")); got != SubSameSettings || note != "added: scy" {
		t.Fatalf("vmess with an added field = %d %q", got, note)
	}
}

func TestRunCLIVerifyAndCompare(t *testing.T) {
	path := newDB(t, legacySchema, fixture...)
	dir := t.TempDir()
	pre := filepath.Join(dir, "pre.json")
	var out, errOut bytes.Buffer
	if code := RunCLI([]string{"inspect", "-db", path}, &out, &errOut); code != 0 {
		t.Fatalf("inspect: %d %s", code, errOut.String())
	}
	if err := os.WriteFile(pre, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := RunCLI([]string{"verify", "-pre", pre, "-db", path}, &out, &errOut); code != 0 {
		t.Fatalf("verify of the same database: %d\n%s", code, out.String())
	}
	mutate(t, path, `DELETE FROM inbounds WHERE id = 1`)
	out.Reset()
	if code := RunCLI([]string{"verify", "-pre", pre, "-db", path}, &out, &errOut); code != 1 || !strings.Contains(out.String(), "FAIL") {
		t.Fatalf("verify after losing an inbound: %d\n%s", code, out.String())
	}
	if code := RunCLI([]string{"verify", "-db", path}, io.Discard, io.Discard); code != 2 {
		t.Fatalf("verify without -pre = %d, want 2", code)
	}

	out.Reset()
	if code := RunCLI([]string{"inspect", "-db", path, "-format", "env"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "inbounds\t4\n") {
		t.Fatalf("inspect -format env: %d\n%s", code, out.String())
	}

	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	_ = os.WriteFile(a, []byte("trojan://p@h:1#x\n"), 0o600)
	_ = os.WriteFile(b, []byte("trojan://p@h:1#y\n"), 0o600)
	if code := RunCLI([]string{"subcompare", "-a", a, "-b", a}, io.Discard, io.Discard); code != SubIdentical {
		t.Fatalf("subcompare same = %d", code)
	}
	if code := RunCLI([]string{"subcompare", "-a", a, "-b", b}, io.Discard, io.Discard); code != SubRemarksDiffer {
		t.Fatalf("subcompare remarks = %d", code)
	}

	out.Reset()
	if code := RunCLI([]string{"version"}, &out, io.Discard); code != 0 || strings.TrimSpace(out.String()) != config.GetUpstreamVersion() {
		t.Fatalf("version = %d %q", code, out.String())
	}
}
