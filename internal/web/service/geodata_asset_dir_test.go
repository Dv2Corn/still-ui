package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// An in-process build points the core's asset variable at the bin folder; that value is the
// panel's own default, so it must not hide a later bin folder or an operator's own folder.
func TestAssetDirFollowsTheBinFolderAfterAnInProcessBuild(t *testing.T) {
	t.Setenv("XRAY_LOCATION_ASSET", "")
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if _, err := xray.BuildInbound([]byte(`{"port":443,"protocol":"vless","settings":{"clients":[],"decryption":"none"}}`), nil); err != nil {
		t.Fatal(err)
	}
	second := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", second)
	if got := assetDir(); got != second {
		t.Fatalf("assetDir = %q, want the current bin folder %q", got, second)
	}
	shared := t.TempDir()
	t.Setenv("XRAY_LOCATION_ASSET", shared)
	if got := assetDir(); got != shared {
		t.Fatalf("assetDir = %q, want the operator's %q", got, shared)
	}
}
