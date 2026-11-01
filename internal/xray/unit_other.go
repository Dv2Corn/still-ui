//go:build !linux

package xray

import (
	"os"
	"time"
)

// Xray runs as its own unit on Linux only (config.GetXrayUnit).

func fileChangedAt(info os.FileInfo) time.Time { return info.ModTime() }

func readableByGroupOf(os.FileInfo, os.FileInfo) bool { return true }

func mkdirAllGroupReadable(dir string) error { return os.MkdirAll(dir, 0o750) }
