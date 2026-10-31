//go:build !windows

package xray

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenLogFileRefusesLinksPlantedByTheCore(t *testing.T) {
	t.Setenv("XUI_XRAY_UNIT", "still-xray-test")
	if !UnitMode() {
		t.Skip("systemd units are Linux only")
	}
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(victim, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "access.log")
	if err := os.WriteFile(plain, []byte("log line\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "error.log")
	if err := os.Symlink(victim, symlink); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(dir, "other.log")
	if err := os.Link(victim, hardlink); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{symlink, hardlink} {
		if f, err := OpenLogFile(path, os.O_WRONLY); err == nil {
			_ = f.Truncate(0)
			_ = f.Close()
			t.Errorf("%s was opened for writing", filepath.Base(path))
		}
	}
	if data, _ := os.ReadFile(victim); string(data) != "secret" {
		t.Fatalf("the link target was changed: %q", data)
	}
	f, err := OpenLogFile(plain, os.O_RDONLY)
	if err != nil {
		t.Fatalf("a plain log the core owns was refused: %v", err)
	}
	_ = f.Close()
}
