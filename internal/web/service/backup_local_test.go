package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/mhsanaei/3x-ui/v3/internal/backup"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// backupTestPanel opens a fresh SQLite panel database in its own folder, with backups beside it.
func backupTestPanel(t *testing.T) (dbDir string) {
	t.Helper()
	if config.GetDBKind() == "postgres" {
		t.Skip("SQLite backup and restore")
	}
	dbDir = t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	t.Setenv("XUI_BACKUP_FOLDER", filepath.Join(dbDir, "backups"))
	dbtest.InitDB(t, config.GetDBPath())
	lastBackup.Store(nil)
	t.Cleanup(func() { lastBackup.Store(nil); BackupTelegramSender = nil })
	return dbDir
}

func setSetting(t *testing.T, key, value string) {
	t.Helper()
	if err := (&SettingService{}).saveSetting(key, value); err != nil {
		t.Fatal(err)
	}
}

func addMarkerInbound(t *testing.T, remark string, port int) {
	t.Helper()
	ib := &model.Inbound{Remark: remark, Port: port, Protocol: model.VLESS, Tag: remark, Enable: true, Settings: `{"clients":[]}`}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
}

func inboundRemarks(t *testing.T) []string {
	t.Helper()
	var rows []model.Inbound
	if err := database.GetDB().Order("port").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Remark)
	}
	return out
}

func TestLocalBackupEncryptRotateRestore(t *testing.T) {
	backupTestPanel(t)
	owner, _ := age.GenerateX25519Identity()
	setSetting(t, "backupAgeRecipients", owner.Recipient().String())
	setSetting(t, "backupKeep", "2")
	addMarkerInbound(t, "before-backup", 20001)

	clock := time.Date(2026, 11, 4, 3, 0, 0, 0, time.Local)
	backupNow = func() time.Time { return clock }
	t.Cleanup(func() { backupNow = time.Now })

	svc := &LocalBackupService{}
	var first *LocalBackupResult
	for i := range 3 {
		clock = clock.Add(time.Hour)
		res, err := svc.Run()
		if err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
		if !res.Encrypted || !strings.HasSuffix(res.File, ".db.age") || res.Size == 0 {
			t.Fatalf("backup %d = %+v, want an encrypted .db.age", i, res)
		}
		if first == nil {
			first = res
		}
	}
	status, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Backups) != 2 || status.Last == nil || status.Last.Error != "" {
		t.Fatalf("status = %+v, want 2 kept backups and a good last run", status)
	}
	for _, e := range status.Backups {
		if e.Name == first.File {
			t.Fatal("the oldest backup was not rotated out")
		}
	}
	newest := filepath.Join(status.Folder, status.Backups[0].Name)
	info, _ := os.Stat(newest)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %o, want 600", info.Mode().Perm())
	}
	dirInfo, _ := os.Stat(status.Folder)
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("backup folder mode %o, want 700", dirInfo.Mode().Perm())
	}
	raw, _ := os.ReadFile(newest)
	if !backup.IsEncrypted(raw) {
		t.Fatal("the backup on disk is not an age file")
	}

	// Changes after the backup; then restore it with the panel stopped.
	addMarkerInbound(t, "after-backup", 20002)
	if err := database.CloseDB(); err != nil {
		t.Fatal(err)
	}

	if _, err := RestoreLocalBackup(newest, nil); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("restore without a key: %v", err)
	}
	stranger, _ := age.GenerateX25519Identity()
	if _, err := RestoreLocalBackup(newest, []age.Identity{stranger}); err == nil {
		t.Fatal("restore with the wrong key succeeded")
	}
	// Neither failure touched the database.
	if err := database.InitDB(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	if got := inboundRemarks(t); len(got) != 2 {
		t.Fatalf("a failed restore changed the database: %v", got)
	}
	_ = database.CloseDB()

	// A stale WAL of the current database must not be replayed into the restored one.
	if err := os.WriteFile(config.GetDBPath()+"-shm", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := RestoreLocalBackup(newest, []age.Identity{owner})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != "sqlite" || res.Previous == "" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(config.GetDBPath() + "-shm"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the old -shm file was left beside the restored database")
	}
	if err := database.InitDB(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	if got := inboundRemarks(t); len(got) != 1 || got[0] != "before-backup" {
		t.Fatalf("restored inbounds = %v, want only the one from before the backup", got)
	}
	_ = database.CloseDB()

	// The database as it was before the restore is kept, complete.
	if err := database.InitDB(res.Previous); err != nil {
		t.Fatal(err)
	}
	if got := inboundRemarks(t); len(got) != 2 {
		t.Fatalf("kept copy has %v, want both inbounds", got)
	}
}

func TestLocalBackupPlainAndRestoreRefusesForeignFiles(t *testing.T) {
	dbDir := backupTestPanel(t)
	addMarkerInbound(t, "plain", 20003)
	res, err := (&LocalBackupService{}).Run()
	if err != nil {
		t.Fatal(err)
	}
	if res.Encrypted || !strings.HasSuffix(res.File, ".db") {
		t.Fatalf("result %+v, want an unencrypted .db", res)
	}
	path := filepath.Join(config.GetBackupFolderPath(), res.File)
	if err := database.ValidateSQLiteDB(path); err != nil {
		t.Fatalf("the backup is not a sound SQLite file: %v", err)
	}
	_ = database.CloseDB()

	junk := filepath.Join(dbDir, "junk.bin")
	if err := os.WriteFile(junk, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreLocalBackup(junk, nil); err == nil || !strings.Contains(err.Error(), "not a still-ui backup") {
		t.Fatalf("junk restore: %v", err)
	}
	pg := filepath.Join(dbDir, "x.pgdump")
	if err := os.WriteFile(pg, []byte("PGDMP\x01\x10"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreLocalBackup(pg, nil); err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("pg dump on SQLite: %v", err)
	}
	// A plain backup restores without a key.
	if _, err := RestoreLocalBackup(path, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLocalBackupTelegram(t *testing.T) {
	backupTestPanel(t)
	setSetting(t, "backupTelegram", "true")

	var sent []string
	BackupTelegramSender = func(name string, data []byte) error {
		if !backup.IsEncrypted(data) {
			t.Error("a plain backup reached Telegram")
		}
		sent = append(sent, name)
		return nil
	}
	res, err := (&LocalBackupService{}).Run()
	if err != nil || !strings.HasPrefix(res.Telegram, "skipped") || len(sent) != 0 {
		t.Fatalf("unencrypted: res=%+v err=%v sent=%v; want skipped, nothing sent", res, err, sent)
	}

	owner, _ := age.GenerateX25519Identity()
	setSetting(t, "backupAgeRecipients", owner.Recipient().String())
	res, err = (&LocalBackupService{}).Run()
	if err != nil || res.Telegram != "sent" || len(sent) != 1 || sent[0] != res.File {
		t.Fatalf("encrypted: res=%+v err=%v sent=%v", res, err, sent)
	}

	BackupTelegramSender = func(string, []byte) error { return ErrTelegramOff }
	if res, err = (&LocalBackupService{}).Run(); err != nil || !strings.HasPrefix(res.Telegram, "skipped") {
		t.Fatalf("bot stopped: res=%+v err=%v; want skipped, not a failure", res, err)
	}

	BackupTelegramSender = func(string, []byte) error { return errors.New("429 Too Many Requests") }
	res, err = (&LocalBackupService{}).Run()
	if err == nil || !strings.Contains(res.Error, "429") || res.File == "" {
		t.Fatalf("upload failure: res=%+v err=%v; want an error and the local file kept", res, err)
	}
	if c, ok := backupCheck(); !ok || !c.Problem || !strings.Contains(c.Params["Error"], "429") {
		t.Fatalf("backup alert = %+v, %v; want a problem naming the error", c, ok)
	}
}

func TestLocalBackupBadRecipientFails(t *testing.T) {
	backupTestPanel(t)
	setSetting(t, "backupAgeRecipients", "age1broken")
	res, err := (&LocalBackupService{}).Run()
	if err == nil || res.File != "" || LastLocalBackup() == nil || LastLocalBackup().Error == "" {
		t.Fatalf("res=%+v err=%v; want a recorded failure and no file", res, err)
	}
	entries, _ := backup.List(config.GetBackupFolderPath())
	if len(entries) != 0 {
		t.Fatalf("a failed run left backups: %v", entries)
	}
}

func TestRemoveStaleBackupWork(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, ".work-123")
	fresh := filepath.Join(dir, ".work-456")
	tmp := filepath.Join(dir, ".tmp-x.db.age-1")
	for _, d := range []string{old, fresh} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(tmp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(tmp, past, past)
	removeStaleBackupWork(dir)
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Error("an hour-old work folder was kept")
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Error("an hour-old temporary file was kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a work folder in use was removed")
	}
}
