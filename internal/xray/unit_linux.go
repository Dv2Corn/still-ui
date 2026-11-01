package xray

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// fileChangedAt is the inode change time: a binary replaced by rename gets a new one, while
// tar keeps the release's old mtime.
func fileChangedAt(info os.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Ctim.Sec, st.Ctim.Nsec)
	}
	return info.ModTime()
}

// readableByGroupOf reports whether the unit's core, a member of folder's group only, can read info.
func readableByGroupOf(info, folder os.FileInfo) bool {
	perm := info.Mode().Perm()
	if perm&0o004 != 0 {
		return true
	}
	file, ok1 := info.Sys().(*syscall.Stat_t)
	dir, ok2 := folder.Sys().(*syscall.Stat_t)
	return ok1 && ok2 && file.Gid == dir.Gid && perm&0o040 != 0
}

// mkdirAllGroupReadable creates dir 0750 despite the panel's UMask=0077: a child with its own
// umask does the mkdir, so a new folder also inherits setgid and group from a setgid parent.
func mkdirAllGroupReadable(dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/sh", "-c", `umask 027 && exec mkdir -p -- "$1"`, "sh", dir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mkdir %s: %w: %s", dir, err, strings.TrimSpace(string(out)))
	}
	return nil
}
