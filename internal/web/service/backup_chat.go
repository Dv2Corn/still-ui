package service

import (
	"errors"
	"fmt"
	"os"

	"github.com/mhsanaei/3x-ui/v3/internal/backup"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// ChatBackupSource is what a bot reads a backup from: the panel database and its file name.
// ServerService is the real one; tests and the Discord service hand in their own.
type ChatBackupSource interface {
	GetDb() ([]byte, error)
	BackupFilename(requestHost string) string
}

// ChatBackupFile is one file of a backup posted to a chat.
type ChatBackupFile struct {
	Name string
	Data []byte
}

// ChatBackup is a backup ready to post to a chat (Telegram, Discord): the database and, when it
// can be read, the generated Xray config. With backup encryption keys set both are age files.
type ChatBackup struct {
	Files     []ChatBackupFile
	Encrypted bool
}

var (
	// ErrChatBackupRead means the database could not be read: nothing is sent.
	ErrChatBackupRead = errors.New("cannot read the database for the backup")
	// ErrChatBackupEncrypt means encryption keys are set but encryption failed: nothing is sent,
	// a plain copy never goes out instead.
	ErrChatBackupEncrypt = errors.New("cannot encrypt the backup with the configured keys")
	// ErrChatBackupPlain is what CheckChatBackup returns for a plain file while keys are set.
	ErrChatBackupPlain = errors.New("refusing to send an unencrypted backup: backup encryption keys are set")
)

// chatBackupConfigPath is a seam for tests.
var chatBackupConfigPath = xray.GetConfigPath

// backupRecipientsSet reports whether backup encryption keys are configured; an unreadable or
// invalid setting is an error, never "no keys".
func backupRecipientsSet() (bool, error) {
	text, err := (&SettingService{}).GetBackupAgeRecipients()
	if err != nil {
		return false, err
	}
	recipients, err := backup.ParseRecipients(text)
	if err != nil {
		return false, err
	}
	return len(recipients) > 0, nil
}

// PrepareChatBackup is the one way a bot gets backup bytes to post: it reads the database and
// the Xray config and, when backup encryption keys are set, encrypts both to them (names get
// ".age"). A failed encryption returns ErrChatBackupEncrypt and no bytes at all.
func PrepareChatBackup(src ChatBackupSource) (*ChatBackup, error) {
	db, err := src.GetDb()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChatBackupRead, err)
	}
	if len(db) == 0 {
		return nil, ErrChatBackupRead
	}
	name := src.BackupFilename("")
	if name == "" {
		name = "x-ui.db"
	}
	files := []ChatBackupFile{{Name: name, Data: db}}
	if config, err := os.ReadFile(chatBackupConfigPath()); err == nil && len(config) > 0 {
		// The generated config travels with the DB: a restore from the file would lose it otherwise.
		files = append(files, ChatBackupFile{Name: "config.json", Data: config})
	} else if err != nil {
		logger.Warning("Cannot read the Xray config for the backup: ", err)
	}

	text, err := (&SettingService{}).GetBackupAgeRecipients()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChatBackupEncrypt, err)
	}
	recipients, err := backup.ParseRecipients(text)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChatBackupEncrypt, err)
	}
	if len(recipients) == 0 {
		return &ChatBackup{Files: files}, nil
	}
	for i := range files {
		enc, err := backup.EncryptBytes(files[i].Data, recipients)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrChatBackupEncrypt, err)
		}
		files[i] = ChatBackupFile{Name: files[i].Name + backup.ExtEncrypted, Data: enc}
	}
	return &ChatBackup{Files: files, Encrypted: true}, nil
}

// CheckChatBackup is the last check before backup bytes leave the server: while encryption keys
// are set every file must be an age file, so a path that skipped PrepareChatBackup still cannot
// post a plain copy. It also reports whether the backup is encrypted.
func CheckChatBackup(b *ChatBackup) (encrypted bool, err error) {
	if b == nil || len(b.Files) == 0 {
		return false, ErrChatBackupRead
	}
	encrypted = true
	for _, f := range b.Files {
		if !backup.IsEncrypted(f.Data) {
			encrypted = false
		}
	}
	if encrypted {
		return true, nil
	}
	set, err := backupRecipientsSet()
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrChatBackupEncrypt, err)
	}
	if set {
		return false, ErrChatBackupPlain
	}
	return false, nil
}
