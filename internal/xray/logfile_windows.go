package xray

import "os"

// OpenLogFile opens an Xray log for the panel; Windows has no separate core unit.
func OpenLogFile(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag, 0)
}
