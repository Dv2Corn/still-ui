//go:build !windows

package service

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The panel runs with UMask=0077; a geofile installed 0600 cannot be read by the
// still-xray core, which then fails to start with every config, the last good one included.
func TestUpdateGeofileInstallsAFileTheCoreUserCanRead(t *testing.T) {
	srv, _ := geofileServer(t, []fakeUpstream{{repo: "a", assets: map[string]string{"geoip.dat": "good geoip payload"}}})
	binFolder := geofileTestEnv(t, map[string]geofileEntry{
		"geoip.dat": {srv.URL + "/a", "geoip.dat", "geoip.dat"},
	})
	var restarted bool
	restartStub(t, &restarted)

	previous := syscall.Umask(0o077)
	err := (&ServerService{}).UpdateGeofile("")
	syscall.Umask(previous)
	if err != nil {
		t.Fatalf("UpdateGeofile: %v", err)
	}

	info, err := os.Stat(filepath.Join(binFolder, "geoip.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("installed geofile mode %o, want 644 so the still-xray user can read it", got)
	}
}
