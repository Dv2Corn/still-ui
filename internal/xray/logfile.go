//go:build !windows

package xray

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// OpenLogFile opens an Xray log for the panel. A unit's core runs as another user and owns
// its log folder, so there the panel (root) never follows a link the core could plant and
// only accepts a single-link regular file owned by the folder's owner.
func OpenLogFile(path string, flag int) (*os.File, error) {
	if !UnitMode() {
		return os.OpenFile(path, flag, 0)
	}
	f, err := os.OpenFile(path, flag|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := checkCoreLogFile(f, filepath.Dir(path)); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func checkCoreLogFile(f *os.File, dir string) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	dirInfo, dirErr := os.Lstat(dir)
	if !ok || dirErr != nil || !info.Mode().IsRegular() || st.Nlink != 1 {
		return fmt.Errorf("refusing xray log %s: not a plain regular file", f.Name())
	}
	if dirSt, ok := dirInfo.Sys().(*syscall.Stat_t); !ok || dirSt.Uid != st.Uid {
		return fmt.Errorf("refusing xray log %s: not owned by the log folder's owner", f.Name())
	}
	return nil
}
