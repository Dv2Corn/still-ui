package backup

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"
	"golang.org/x/crypto/ssh"
)

func TestFileNameIsSafeAndListable(t *testing.T) {
	at := time.Date(2026, 11, 4, 3, 30, 0, 0, time.Local)
	cases := map[string]string{
		"vpn.example.com":  "still-ui-vpn.example.com-20261104-033000.db",
		"../../etc/passwd": "still-ui-etc-passwd-20261104-033000.db",
		"":                 "still-ui-panel-20261104-033000.db",
		"host name/x":      "still-ui-host-name-x-20261104-033000.db",
	}
	for host, want := range cases {
		if got := FileName(host, at, ExtSQLite, false); got != want {
			t.Errorf("FileName(%q) = %q, want %q", host, got, want)
		}
		if _, ok := parseName(want); !ok {
			t.Errorf("%q is not recognised as a backup", want)
		}
	}
	if got := FileName("h", at, ExtPostgres, true); got != "still-ui-h-20261104-033000.pgdump.age" {
		t.Errorf("encrypted postgres name = %q", got)
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPruneKeepsNewestAndLeavesForeignFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 1, 3, 0, 0, 0, time.Local)
	var names []string
	for i := range 5 {
		name := FileName("h", base.Add(time.Duration(i)*24*time.Hour), ExtSQLite, i%2 == 0)
		names = append(names, name)
		writeFile(t, filepath.Join(dir, name), "x")
	}
	writeFile(t, filepath.Join(dir, "notes.txt"), "mine")
	writeFile(t, filepath.Join(dir, "still-ui-h-garbage.db"), "mine too")

	removed, err := Prune(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(removed)
	if want := names[:3]; !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
	entries, _ := List(dir)
	if len(entries) != 2 || entries[0].Name != names[4] || entries[1].Name != names[3] {
		t.Fatalf("left %+v, want the two newest, newest first", entries)
	}
	if !entries[0].Encrypted || entries[1].Encrypted {
		t.Fatalf("encrypted flags wrong: %+v", entries)
	}
	for _, keep := range []string{"notes.txt", "still-ui-h-garbage.db"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s was removed: %v", keep, err)
		}
	}
	// keep below 1 still keeps the newest one.
	if _, err := Prune(dir, 0); err != nil {
		t.Fatal(err)
	}
	if entries, _ := List(dir); len(entries) != 1 {
		t.Fatalf("Prune(0) left %d backups, want 1", len(entries))
	}
}

func TestListMissingFolder(t *testing.T) {
	entries, err := List(filepath.Join(t.TempDir(), "nope"))
	if err != nil || entries != nil {
		t.Fatalf("List(missing) = %v, %v", entries, err)
	}
}

func sshKeys(t *testing.T) (pub string, privPEM []byte) {
	t.Helper()
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pk)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(sk, "")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), pem.EncodeToMemory(block)
}

func TestParseRecipients(t *testing.T) {
	id1, _ := age.GenerateX25519Identity()
	id2, _ := age.GenerateX25519Identity()
	sshPub, _ := sshKeys(t)

	if r, err := ParseRecipients("  \n# nothing\n"); err != nil || r != nil {
		t.Fatalf("empty value = %v, %v; want no encryption", r, err)
	}
	text := id1.Recipient().String() + ", " + id2.Recipient().String() + "\n# laptop\n" + sshPub + "\n"
	r, err := ParseRecipients(text)
	if err != nil || len(r) != 3 {
		t.Fatalf("ParseRecipients = %d, %v; want 3", len(r), err)
	}

	_, err = ParseRecipients(id1.String())
	if err == nil || strings.Contains(err.Error(), id1.String()) || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("a pasted private key must be refused without echoing it, got %v", err)
	}
	if _, err := ParseRecipients("age1notakey"); err == nil {
		t.Fatal("a broken key was accepted")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.db")
	payload := bytes.Repeat([]byte("SQLite format 3\x00 panel data "), 5000)
	if err := os.WriteFile(plain, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	owner, _ := age.GenerateX25519Identity()
	stranger, _ := age.GenerateX25519Identity()
	sshPub, sshPriv := sshKeys(t)
	recipients, err := ParseRecipients(owner.Recipient().String() + "\n" + sshPub)
	if err != nil {
		t.Fatal(err)
	}
	enc := filepath.Join(dir, "b.db.age")
	if err := EncryptFile(plain, enc, recipients); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(enc)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("encrypted file mode %o, want 600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(enc)
	if !IsEncrypted(data) || bytes.Contains(data, []byte("panel data")) {
		t.Fatal("the backup is not encrypted")
	}

	for name, key := range map[string][]byte{"age": []byte(owner.String() + "\n"), "ssh": sshPriv} {
		ids, err := ParseIdentities(key)
		if err != nil {
			t.Fatalf("%s identity: %v", name, err)
		}
		out := filepath.Join(dir, "out-"+name)
		if err := DecryptFile(enc, out, ids); err != nil {
			t.Fatalf("%s decrypt: %v", name, err)
		}
		got, _ := os.ReadFile(out)
		if !bytes.Equal(got, payload) {
			t.Fatalf("%s decrypt returned other bytes", name)
		}
	}

	ids, _ := ParseIdentities([]byte(stranger.String()))
	wrong := filepath.Join(dir, "wrong")
	if err := DecryptFile(enc, wrong, ids); err == nil || !strings.Contains(err.Error(), "another recipient") {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := os.Stat(wrong); !os.IsNotExist(err) {
		t.Fatal("a failed decrypt left a file behind")
	}

	// A damaged file fails and leaves nothing under the final name.
	data[len(data)-10] ^= 0xff
	if err := os.WriteFile(enc, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ownerIDs, _ := ParseIdentities([]byte(owner.String()))
	broken := filepath.Join(dir, "broken")
	if err := DecryptFile(enc, broken, ownerIDs); err == nil {
		t.Fatal("a damaged backup decrypted")
	}
	if _, err := os.Stat(broken); !os.IsNotExist(err) {
		t.Fatal("a damaged backup left a file behind")
	}
}

func TestDecryptArmored(t *testing.T) {
	dir := t.TempDir()
	id, _ := age.GenerateX25519Identity()
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("hello"))
	_ = w.Close()
	_ = aw.Close()
	src := filepath.Join(dir, "a.age")
	if err := os.WriteFile(src, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(buf.Bytes()) {
		t.Fatal("armored file not recognised")
	}
	out := filepath.Join(dir, "out")
	if err := DecryptFile(src, out, []age.Identity{id}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestEncryptBytes(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	out, err := EncryptBytes([]byte("config"), []age.Recipient{id.Recipient()})
	if err != nil || !IsEncrypted(out) {
		t.Fatalf("EncryptBytes: %v", err)
	}
	r, err := age.Decrypt(bytes.NewReader(out), id)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	_, _ = got.ReadFrom(r)
	if got.String() != "config" {
		t.Fatalf("got %q", got.String())
	}
	if err := EncryptFile("x", "y", nil); err == nil {
		t.Fatal("encrypting to nobody must fail")
	}
}
