package service

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/mhsanaei/3x-ui/v3/internal/backup"
)

type fakeChatBackupSource struct {
	db  []byte
	err error
}

func (f fakeChatBackupSource) GetDb() ([]byte, error)       { return f.db, f.err }
func (f fakeChatBackupSource) BackupFilename(string) string { return "panel_2026-11-05_120000.db" }

// chatBackupFixture opens a panel database and points the Xray config at a file of known bytes.
func chatBackupFixture(t *testing.T) (config []byte) {
	t.Helper()
	backupTestPanel(t)
	config = []byte(`{"inbounds":[{"tag":"reality","settings":{"privateKey":"secret"}}]}`)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		t.Fatal(err)
	}
	prev := chatBackupConfigPath
	chatBackupConfigPath = func() string { return path }
	t.Cleanup(func() { chatBackupConfigPath = prev })
	return config
}

func decryptAge(t *testing.T, data []byte, id age.Identity) []byte {
	t.Helper()
	r, err := age.Decrypt(bytes.NewReader(data), id)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// With encryption keys set, every file a bot may post is an age file to those keys, and it
// decrypts to exactly the database and config the panel holds.
func TestPrepareChatBackupEncryptsEveryFileWhenKeysAreSet(t *testing.T) {
	config := chatBackupFixture(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	setSetting(t, "backupAgeRecipients", id.Recipient().String())
	db := []byte("SQLite format 3\x00 the whole panel")

	b, err := PrepareChatBackup(fakeChatBackupSource{db: db})
	if err != nil {
		t.Fatal(err)
	}
	if !b.Encrypted || len(b.Files) != 2 {
		t.Fatalf("backup = encrypted %v, %d files; want encrypted, DB and config", b.Encrypted, len(b.Files))
	}
	want := map[string][]byte{"panel_2026-11-05_120000.db.age": db, "config.json.age": config}
	for _, f := range b.Files {
		if !backup.IsEncrypted(f.Data) {
			t.Fatalf("%s is not an age file", f.Name)
		}
		plain, ok := want[f.Name]
		if !ok {
			t.Fatalf("unexpected file %q", f.Name)
		}
		if got := decryptAge(t, f.Data, id); !bytes.Equal(got, plain) {
			t.Fatalf("%s decrypts to %q, want %q", f.Name, got, plain)
		}
	}
	if enc, err := CheckChatBackup(b); err != nil || !enc {
		t.Fatalf("CheckChatBackup = %v, %v; want encrypted", enc, err)
	}
}

// Without keys the copy stays plain, as before, and says so.
func TestPrepareChatBackupWithoutKeysIsPlain(t *testing.T) {
	config := chatBackupFixture(t)
	b, err := PrepareChatBackup(fakeChatBackupSource{db: []byte("db")})
	if err != nil {
		t.Fatal(err)
	}
	if b.Encrypted || len(b.Files) != 2 || string(b.Files[0].Data) != "db" || !bytes.Equal(b.Files[1].Data, config) {
		t.Fatalf("plain backup = %+v", b)
	}
	if enc, err := CheckChatBackup(b); err != nil || enc {
		t.Fatalf("CheckChatBackup = %v, %v; want plain and allowed", enc, err)
	}
}

// A broken key setting never turns into a plain copy: nothing is returned.
func TestPrepareChatBackupNeverFallsBackToPlain(t *testing.T) {
	chatBackupFixture(t)
	setSetting(t, "backupAgeRecipients", "not-a-key")
	b, err := PrepareChatBackup(fakeChatBackupSource{db: []byte("db")})
	if !errors.Is(err, ErrChatBackupEncrypt) || b != nil {
		t.Fatalf("PrepareChatBackup = %+v, %v; want ErrChatBackupEncrypt and no bytes", b, err)
	}
	if _, err := PrepareChatBackup(fakeChatBackupSource{err: errors.New("disk")}); !errors.Is(err, ErrChatBackupRead) {
		t.Fatalf("unreadable DB: %v, want ErrChatBackupRead", err)
	}
}

// The last check refuses a plain file while keys are set, whoever built it.
func TestCheckChatBackupRefusesPlainWhileKeysAreSet(t *testing.T) {
	chatBackupFixture(t)
	id, _ := age.GenerateX25519Identity()
	setSetting(t, "backupAgeRecipients", id.Recipient().String())
	plain := &ChatBackup{Files: []ChatBackupFile{{Name: "x-ui.db", Data: []byte("SQLite format 3\x00")}}}
	if _, err := CheckChatBackup(plain); !errors.Is(err, ErrChatBackupPlain) {
		t.Fatalf("CheckChatBackup(plain) = %v, want ErrChatBackupPlain", err)
	}
	if _, err := CheckChatBackup(nil); !errors.Is(err, ErrChatBackupRead) {
		t.Fatalf("CheckChatBackup(nil) = %v, want ErrChatBackupRead", err)
	}
}
