//go:build unix

package panelmigrate

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

// The backup holds every secret of the panel: it must never exist, even for a
// moment, with a mode wider than 0600, whatever the umask.
func TestBackupSQLiteNeverExposesTheCopy(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	src := filepath.Join(t.TempDir(), "x-ui.db")
	db, err := sql.Open("sqlite3", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatal(err)
	}
	// Big enough that VACUUM INTO takes a while to write.
	blob := strings.Repeat("x", 4096)
	tx, _ := db.Begin()
	for i := range 4000 {
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, fmt.Sprint("k", i), blob); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "backup.db")
	var stop atomic.Bool
	seen := make(chan string, 1)
	go func() {
		for !stop.Load() {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				// Anything readable by others in the target folder, the copy
				// itself or a file next to it.
				if info, err := os.Lstat(filepath.Join(dir, e.Name())); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0 {
					select {
					case seen <- fmt.Sprintf("%s %v", e.Name(), info.Mode().Perm()):
					default:
					}
				}
			}
		}
	}()
	err = BackupSQLite(src, dst)
	stop.Store(true)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seen:
		t.Fatalf("the backup was readable by others while it was written: %s", got)
	default:
	}
	if info, err := os.Stat(dst); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the staging folder was left behind: %v", entries)
	}
}
