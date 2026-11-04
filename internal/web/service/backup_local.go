package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filippo.io/age"

	"github.com/mhsanaei/3x-ui/v3/internal/backup"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// telegramFileLimit is the largest file a bot may upload through the Bot API.
const telegramFileLimit = 50 << 20

// LocalBackupResult is the outcome of one local backup.
type LocalBackupResult struct {
	File      string `json:"file,omitempty" example:"still-ui-panel-20261104-033000.db.age"`
	Size      int64  `json:"size,omitempty" example:"1048576"`
	Encrypted bool   `json:"encrypted"`
	Time      int64  `json:"time" example:"1793763000"`
	Error     string `json:"error,omitempty"`
	// Telegram is "sent", "skipped: <why>" or "failed: <why>", empty when not asked for.
	Telegram string `json:"telegram,omitempty"`
}

// LocalBackupStatus is what the settings page shows about local backups.
type LocalBackupStatus struct {
	Folder  string             `json:"folder" example:"/etc/still-ui/backups"`
	Last    *LocalBackupResult `json:"last,omitempty"`
	Backups []backup.Entry     `json:"backups"`
}

var (
	backupRunMu sync.Mutex
	lastBackup  atomic.Pointer[LocalBackupResult]
	// BackupTelegramSender uploads a file to the Telegram admins; set by the web layer while the bot runs.
	BackupTelegramSender func(name string, data []byte) error
	// ErrTelegramOff is what BackupTelegramSender returns while the bot is stopped.
	ErrTelegramOff = errors.New("the Telegram bot is not running")
	// backupNow is a seam for tests.
	backupNow = time.Now
)

// LastLocalBackup returns the outcome of the latest local backup since the panel started, or nil.
func LastLocalBackup() *LocalBackupResult { return lastBackup.Load() }

// LocalBackupService takes, encrypts, rotates and restores local database backups.
type LocalBackupService struct {
	settingService SettingService
}

// Status lists the backup folder.
func (s *LocalBackupService) Status() (*LocalBackupStatus, error) {
	dir := config.GetBackupFolderPath()
	entries, err := backup.List(dir)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []backup.Entry{}
	}
	return &LocalBackupStatus{Folder: dir, Last: LastLocalBackup(), Backups: entries}, nil
}

// Run takes one backup now: a consistent snapshot of the database (SQLite backup API, or pg_dump),
// checked, encrypted when recipients are set, rotated, and sent to Telegram when asked for.
func (s *LocalBackupService) Run() (*LocalBackupResult, error) {
	backupRunMu.Lock()
	defer backupRunMu.Unlock()
	res, err := s.run()
	res.Time = backupNow().Unix()
	if err != nil {
		res.Error = err.Error()
		logger.Warning("local backup failed:", err)
	} else {
		logger.Infof("local backup written: %s (%d bytes, encrypted=%v)", res.File, res.Size, res.Encrypted)
	}
	lastBackup.Store(res)
	return res, err
}

func (s *LocalBackupService) run() (*LocalBackupResult, error) {
	res := &LocalBackupResult{}
	recipientsText, err := s.settingService.GetBackupAgeRecipients()
	if err != nil {
		return res, err
	}
	recipients, err := backup.ParseRecipients(recipientsText)
	if err != nil {
		return res, fmt.Errorf("backup encryption: %w", err)
	}
	keep, err := s.settingService.GetBackupKeep()
	if err != nil || keep < 1 {
		keep = 14
	}
	dir := config.GetBackupFolderPath()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return res, err
	}
	_ = os.Chmod(dir, 0o700)
	removeStaleBackupWork(dir)

	host, _ := os.Hostname()
	ext := backup.ExtSQLite
	if database.IsPostgres() {
		ext = backup.ExtPostgres
	}
	name := backup.FileName(host, backupNow(), ext, len(recipients) > 0)
	final := filepath.Join(dir, name)

	work, err := os.MkdirTemp(dir, ".work-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(work)
	plain := filepath.Join(work, "snapshot"+ext)
	if err := writeDBSnapshot(plain); err != nil {
		return res, err
	}
	if len(recipients) > 0 {
		err = backup.EncryptFile(plain, final, recipients)
	} else {
		err = os.Rename(plain, final)
	}
	if err != nil {
		return res, err
	}
	info, err := os.Stat(final)
	if err != nil {
		return res, err
	}
	res.File, res.Size, res.Encrypted = name, info.Size(), len(recipients) > 0
	if removed, err := backup.Prune(dir, keep); err != nil {
		logger.Warning("local backup: could not remove old backups:", err)
	} else if len(removed) > 0 {
		logger.Info("local backup: removed old backups:", strings.Join(removed, ", "))
	}

	if send, _ := s.settingService.GetBackupTelegram(); send {
		res.Telegram = sendBackupToTelegram(final, res)
		if strings.HasPrefix(res.Telegram, "failed") {
			return res, errors.New("telegram upload " + res.Telegram)
		}
	}
	return res, nil
}

// removeStaleBackupWork deletes what a run killed halfway left behind (the panel restarted, the
// disk filled up); only after an hour, so a backup the CLI runs beside the panel is not touched.
func removeStaleBackupWork(dir string) {
	for _, pattern := range []string{".work-*", ".tmp-*"} {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, p := range matches {
			if info, err := os.Lstat(p); err == nil && time.Since(info.ModTime()) > time.Hour {
				_ = os.RemoveAll(p)
			}
		}
	}
}

// sendBackupToTelegram uploads an encrypted backup; a plain one never leaves the server this way.
func sendBackupToTelegram(path string, res *LocalBackupResult) string {
	switch {
	case !res.Encrypted:
		return "skipped: set an encryption key first, unencrypted backups are not sent"
	case BackupTelegramSender == nil:
		return "skipped: the Telegram bot is not running"
	case res.Size > telegramFileLimit:
		return fmt.Sprintf("failed: %d MB is over the Telegram limit of 50 MB", res.Size>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "failed: " + err.Error()
	}
	if err := BackupTelegramSender(filepath.Base(path), data); err != nil {
		if errors.Is(err, ErrTelegramOff) {
			return "skipped: " + err.Error()
		}
		return "failed: " + err.Error()
	}
	return "sent"
}

// writeDBSnapshot writes a consistent copy of the panel database to dst and checks it reads back.
func writeDBSnapshot(dst string) error {
	if database.IsPostgres() {
		return pgDumpToFile(dst)
	}
	if err := database.BackupSQLite(dst); err != nil {
		return fmt.Errorf("sqlite snapshot: %w", err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return err
	}
	if err := database.ValidateSQLiteDB(dst); err != nil {
		return fmt.Errorf("sqlite snapshot failed its integrity check: %w", err)
	}
	return nil
}

func pgDumpToFile(dst string) error {
	bin, err := exec.LookPath("pg_dump")
	if err != nil {
		return errors.New("pg_dump not found: install the PostgreSQL client tools (still-ui pgclient)")
	}
	env, dbname, err := pgConnEnv(config.GetDBDSN())
	if err != nil {
		return fmt.Errorf("invalid PostgreSQL DSN: %w", err)
	}
	cmd := exec.CommandContext(context.Background(), bin, "--format=custom", "--no-owner", "--no-privileges",
		"--file", dst, "--dbname", dbname)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	_ = os.Chmod(dst, 0o600)
	if restore, err := exec.LookPath("pg_restore"); err == nil {
		if err := checkPgRestoreCanRead(restore, dst); err != nil {
			return fmt.Errorf("the dump does not read back: %w", err)
		}
	}
	return nil
}

// EncryptForTelegram encrypts data to the backup recipients when any are set; ok is false
// (data returned as is) when backups are not encrypted.
func (s *LocalBackupService) EncryptForTelegram(data []byte) (out []byte, ok bool, err error) {
	text, err := s.settingService.GetBackupAgeRecipients()
	if err != nil {
		return nil, false, err
	}
	recipients, err := backup.ParseRecipients(text)
	if err != nil {
		return nil, false, err
	}
	if len(recipients) == 0 {
		return data, false, nil
	}
	out, err = backup.EncryptBytes(data, recipients)
	return out, err == nil, err
}

// RestoreResult says what a restore replaced and where the previous database was kept.
type RestoreResult struct {
	Kind     string // "sqlite" or "postgres"
	Previous string // the copy of the database as it was before the restore
}

// RestoreLocalBackup replaces the panel database with a backup file: decrypted with identities
// when it is an age file, checked before anything is touched, the current database kept beside it.
// The panel must be stopped (x-ui.sh restore does that); the next start migrates the restored schema.
func RestoreLocalBackup(file string, identities []age.Identity) (*RestoreResult, error) {
	header, err := readHeader(file)
	if err != nil {
		return nil, err
	}
	stamp := backupNow().Format("20060102-150405")
	workDir := config.GetDBFolderPath()
	if database.IsPostgres() {
		workDir = os.TempDir()
	}
	work, err := os.MkdirTemp(workDir, ".restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	_ = os.Chmod(work, 0o700)

	plain := file
	if backup.IsEncrypted(header) {
		if len(identities) == 0 {
			return nil, errors.New("this backup is encrypted: give the age or SSH private key it was encrypted to (-identity)")
		}
		plain = filepath.Join(work, "backup")
		if err := backup.DecryptFile(file, plain, identities); err != nil {
			return nil, fmt.Errorf("decrypt: %w", err)
		}
		if header, err = readHeader(plain); err != nil {
			return nil, err
		}
	}

	kind := sniffImportKind(header)
	if database.IsPostgres() {
		if kind != importKindPgDump {
			return nil, errors.New("this panel runs PostgreSQL: restore a PostgreSQL backup (.pgdump) here, or import a SQLite database from the panel's Backup & Restore")
		}
		return restorePostgresFile(plain, stamp)
	}
	switch kind {
	case importKindSQLiteDB:
	case importKindPgDump:
		return nil, errors.New("this is a PostgreSQL backup; the panel runs SQLite")
	case importKindSQLiteDump:
		return nil, errors.New("this is a SQL text dump: convert it first with still-ui migrateDB <file.dump>")
	default:
		return nil, errors.New("not a still-ui backup: expected a SQLite database, a PostgreSQL dump or an age file")
	}

	staged := filepath.Join(work, "x-ui.db")
	if err := copyFile0600(plain, staged); err != nil {
		return nil, err
	}
	if err := database.ValidateSQLiteDB(staged); err != nil {
		return nil, fmt.Errorf("the backup is damaged: %w", err)
	}
	if err := database.PrepareSQLiteForMigration(staged); err != nil {
		return nil, fmt.Errorf("the backup is not a panel database: %w", err)
	}

	dbPath := config.GetDBPath()
	res := &RestoreResult{Kind: "sqlite"}
	if _, err := os.Stat(dbPath); err == nil {
		res.Previous = dbPath + ".before-restore-" + stamp
		if err := database.CopySQLiteFile(dbPath, res.Previous); err != nil {
			return nil, fmt.Errorf("could not keep a copy of the current database, nothing was changed: %w", err)
		}
	}
	// A -wal left beside the new file would be replayed into it: drop the old journal first.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Remove(dbPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("could not remove the old %s file, nothing was changed: %w", suffix, err)
		}
	}
	if err := os.Rename(staged, dbPath); err != nil {
		return nil, fmt.Errorf("could not move the restored database into place: %w", err)
	}
	return res, nil
}

func restorePostgresFile(dump, stamp string) (*RestoreResult, error) {
	bin, err := exec.LookPath("pg_restore")
	if err != nil {
		return nil, errors.New("pg_restore not found: install the PostgreSQL client tools (still-ui pgclient)")
	}
	if err := checkPgRestoreCanRead(bin, dump); err != nil {
		return nil, err
	}
	env, dbname, err := pgConnEnv(config.GetDBDSN())
	if err != nil {
		return nil, fmt.Errorf("invalid PostgreSQL DSN: %w", err)
	}
	res := &RestoreResult{Kind: "postgres", Previous: filepath.Join(config.GetDBFolderPath(), "postgres.before-restore-"+stamp+backup.ExtPostgres)}
	if err := pgDumpToFile(res.Previous); err != nil {
		return nil, fmt.Errorf("could not keep a copy of the current database, nothing was changed: %w", err)
	}
	cmd := exec.CommandContext(context.Background(), bin, "--clean", "--if-exists", "--no-owner", "--no-privileges",
		"--single-transaction", "--dbname", dbname, dump)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pg_restore failed, the database is unchanged: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return res, nil
}

func readHeader(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 64)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

func copyFile0600(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
