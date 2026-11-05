package xray

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseXrayVersionTellsStillBuildFromOfficial(t *testing.T) {
	cases := []struct {
		name, out, version string
		still              bool
	}{
		{"still-ui build", "Xray 26.10.31 (Xray, Penetrates Everything.) sp-ios (go1.27.3 linux/amd64)\nA unified platform for anti-censorship.\n", "26.10.31", true},
		{"official release", "Xray 26.10.31 (Xray, Penetrates Everything.) a4a3dff (go1.26.2 linux/amd64)\nA unified platform for anti-censorship.\n", "26.10.31", false},
		{"custom build", "Xray 26.9.9 (Xray, Penetrates Everything.) Custom (go1.26.1 linux/amd64)\n", "26.9.9", false},
		{"name only in a later line", "Xray 26.9.9 (Xray, Penetrates Everything.) Custom (go1.26.1 linux/amd64)\nsp-ios (x)\n", "26.9.9", false},
		{"garbage", "", "Unknown", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			version, still := parseXrayVersion([]byte(tc.out))
			if version != tc.version || still != tc.still {
				t.Fatalf("parseXrayVersion = (%q, %v), want (%q, %v)", version, still, tc.version, tc.still)
			}
		})
	}
}

// The name is linked in by scripts/xray-build/build.sh; if the build changes it,
// the panel would treat its own core as an official one.
func TestStillBuildNameMatchesBuildScript(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "xray-build", "build.sh"))
	if err != nil {
		t.Fatalf("read build.sh: %v", err)
	}
	text := string(script)
	if !strings.Contains(text, "core.build=${XRAY_BUILD_NAME}") {
		t.Fatal("scripts/xray-build/build.sh no longer links XRAY_BUILD_NAME as core.build")
	}
	if want := "\nXRAY_BUILD_NAME=" + StillBuildName + "\n"; !strings.Contains(text, want) {
		t.Fatalf("scripts/xray-build/build.sh does not set XRAY_BUILD_NAME=%s", StillBuildName)
	}
}

func TestRefreshVersionRecordsStillBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	dir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", dir)
	binaryPath := filepath.Join(dir, GetBinaryName())
	write := func(build string) {
		script := "#!/bin/sh\necho 'Xray 26.10.31 (Xray, Penetrates Everything.) " + build + " (go1.27.3 linux/amd64)'\n"
		if err := os.WriteFile(binaryPath, []byte(script), 0o700); err != nil {
			t.Fatalf("write xray fixture: %v", err)
		}
	}

	p := newProcess(&Config{})
	write(StillBuildName)
	p.refreshVersion()
	if !p.IsStillBuild() || p.GetXrayVersion() != "26.10.31" {
		t.Fatalf("still build: IsStillBuild=%v version=%q", p.IsStillBuild(), p.GetXrayVersion())
	}
	write("a4a3dff")
	p.refreshVersion()
	if p.IsStillBuild() {
		t.Fatal("official build reported as the still-ui build")
	}
}
