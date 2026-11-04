package tgbot

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"

	"github.com/mhsanaei/3x-ui/v3/internal/backup"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type sentDocument struct {
	chatID  int64
	name    string
	data    []byte
	caption string
}

// documentServer records every uploaded document with its bytes, and the text of every message.
func documentServer(t *testing.T) (docs func() []sentDocument, texts func() []string) {
	t.Helper()
	var mu sync.Mutex
	var gotDocs []sentDocument
	var gotTexts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := filepath.Base(r.URL.Path)
		var chatID int64
		switch method {
		case "sendDocument":
			if err := r.ParseMultipartForm(1 << 24); err != nil {
				t.Error(err)
				return
			}
			_ = json.Unmarshal([]byte(r.FormValue("chat_id")), &chatID)
			file, header, err := r.FormFile("document")
			if err != nil {
				t.Error(err)
				return
			}
			data, _ := io.ReadAll(file)
			_ = file.Close()
			mu.Lock()
			gotDocs = append(gotDocs, sentDocument{chatID: chatID, name: header.Filename, data: data, caption: r.FormValue("caption")})
			mu.Unlock()
		case "sendMessage":
			var body struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			chatID = body.ChatID
			mu.Lock()
			gotTexts = append(gotTexts, body.Text)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{
			"message_id": 7, "date": 0, "chat": map[string]any{"id": chatID, "type": "private"},
		}})
	}))
	t.Cleanup(srv.Close)
	swapTestBot(t, srv.URL)
	return func() []sentDocument {
			mu.Lock()
			defer mu.Unlock()
			return append([]sentDocument(nil), gotDocs...)
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), gotTexts...)
		}
}

// backupBotFixture is a running bot with admin chat 1, a real SQLite panel database and a
// generated Xray config of known bytes.
func backupBotFixture(t *testing.T) (*Tgbot, []byte) {
	t.Helper()
	if config.GetDBKind() == "postgres" {
		t.Skip("SQLite backup bytes")
	}
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	t.Setenv("XUI_BIN_FOLDER", dir)
	dbtest.InitDB(t, config.GetDBPath())
	conf := []byte(`{"inbounds":[{"tag":"reality","settings":{"privateKey":"reality-private"}}]}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), conf, 0o600); err != nil {
		t.Fatal(err)
	}
	origRunning := isRunning
	t.Cleanup(func() { isRunning = origRunning })
	isRunning = true
	origGap := backupFileGap
	backupFileGap = 0
	t.Cleanup(func() { backupFileGap = origGap })
	setTestAdmins(t, 1)
	notices.reset()
	return &Tgbot{}, conf
}

func setBackupRecipients(t *testing.T, value string) {
	t.Helper()
	if err := database.GetDB().Create(&model.Setting{Key: "backupAgeRecipients", Value: value}).Error; err != nil {
		t.Fatal(err)
	}
}

func decryptDoc(t *testing.T, d sentDocument, id age.Identity) []byte {
	t.Helper()
	if !backup.IsEncrypted(d.data) || !strings.HasSuffix(d.name, ".age") {
		t.Fatalf("%s left the server unencrypted (starts %q)", d.name, d.data[:min(16, len(d.data))])
	}
	r, err := age.Decrypt(bytes.NewReader(d.data), id)
	if err != nil {
		t.Fatalf("decrypt %s: %v", d.name, err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// checkDecryptedDB proves the decrypted file is this panel's database: a valid SQLite file that
// holds the setting the test wrote.
func checkDecryptedDB(t *testing.T, plain []byte, marker string) {
	t.Helper()
	if !bytes.HasPrefix(plain, []byte("SQLite format 3\x00")) || !bytes.Contains(plain, []byte(marker)) {
		t.Fatalf("the decrypted backup is not this panel's database (%d bytes)", len(plain))
	}
	path := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := database.ValidateSQLiteDB(path); err != nil {
		t.Fatalf("the decrypted backup does not open: %v", err)
	}
}

// STILL-BKP-1: with encryption keys set, the Backup button, the scheduled report backup and the
// local-backup upload all send only age files, and they decrypt to the panel's DB and config.
func TestEveryBotBackupPathSendsOnlyEncryptedBytes(t *testing.T) {
	tb, conf := backupBotFixture(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	recipient := id.Recipient().String()
	setBackupRecipients(t, recipient)

	paths := map[string]func(){
		"button":    func() { tb.sendBackupScreen(1) },
		"scheduled": tb.SendBackupToAdmins,
		"local": func() {
			enc, err := backup.EncryptBytes([]byte("SQLite format 3\x00"+recipient), []age.Recipient{id.Recipient()})
			if err != nil {
				t.Fatal(err)
			}
			if err := tb.SendBackupFile("still-ui-panel-20261105-120000.db.age", enc); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, send := range paths {
		t.Run(name, func(t *testing.T) {
			docs, _ := documentServer(t)
			send()
			got := docs()
			if len(got) == 0 {
				t.Fatal("no backup was sent")
			}
			for _, d := range got {
				if d.chatID != 1 {
					t.Fatalf("%s went to chat %d, not the admin chat", d.name, d.chatID)
				}
				plain := decryptDoc(t, d, id)
				switch {
				case strings.HasPrefix(d.name, "config.json"):
					if !bytes.Equal(plain, conf) {
						t.Fatalf("config decrypts to %q, want %q", plain, conf)
					}
				case name == "local":
					if !bytes.HasPrefix(plain, []byte("SQLite format 3\x00")) {
						t.Fatal("the local backup changed on the way")
					}
				default:
					checkDecryptedDB(t, plain, recipient)
				}
				if strings.Contains(d.caption, tb.I18nBot("tgbot.messages.backupNotEncrypted")) {
					t.Fatal("an encrypted copy is labelled as unencrypted")
				}
			}
			if name != "local" && len(got) != 2 {
				t.Fatalf("%d files sent, want the DB and config.json", len(got))
			}
		})
	}
}

// Without keys the button still sends the plain DB, and the caption says it is not encrypted and
// where to turn encryption on.
func TestPlainBackupSaysItIsNotEncrypted(t *testing.T) {
	tb, _ := backupBotFixture(t)
	docs, _ := documentServer(t)
	tb.sendBackupScreen(1)
	got := docs()
	if len(got) != 2 || !bytes.HasPrefix(got[0].data, []byte("SQLite format 3\x00")) {
		t.Fatalf("plain backup = %d files", len(got))
	}
	if note := tb.I18nBot("tgbot.messages.backupNotEncrypted"); note == "" || !strings.Contains(got[0].caption, strings.TrimSpace(note)) {
		t.Fatalf("caption %q does not warn that the copy is not encrypted", got[0].caption)
	}
}

// Broken keys never fall back to a plain copy: nothing is sent and the admin is told why.
func TestBackupWithBrokenKeysSendsNothing(t *testing.T) {
	tb, _ := backupBotFixture(t)
	setBackupRecipients(t, "age1-not-a-key")
	docs, texts := documentServer(t)
	tb.sendBackupScreen(1)
	tb.SendBackupToAdmins()
	if n := len(docs()); n != 0 {
		t.Fatalf("%d files sent with unusable encryption keys, want 0", n)
	}
	want := tb.I18nBot("tgbot.messages.backupEncryptFailed")
	found := 0
	for _, text := range texts() {
		if strings.Contains(text, want) {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("encryption failure reported %d times, want 2 (texts: %q)", found, texts())
	}
}

// A backup goes only to the configured admin chats, whoever asks for it, and a plain file never
// leaves through the local-backup upload.
func TestBackupGoesOnlyToAdminChats(t *testing.T) {
	tb, _ := backupBotFixture(t)
	docs, _ := documentServer(t)
	tb.sendBackupScreen(42)
	if n := len(docs()); n != 0 {
		t.Fatalf("%d files sent to a chat that is not an admin chat", n)
	}
	if err := tb.SendBackupFile("x.db", []byte("SQLite format 3\x00")); err == nil || len(docs()) != 0 {
		t.Fatal("SendBackupFile sent a plain file")
	}
}
