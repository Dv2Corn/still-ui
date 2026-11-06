//go:build linux

package xray

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

// uptimeMicros is now on the clock systemd's ExecMainStartTimestampMonotonic uses.
func uptimeMicros(t *testing.T) uint64 {
	t.Helper()
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		t.Fatal(err)
	}
	secs, err := strconv.ParseFloat(strings.Fields(string(raw))[0], 64)
	if err != nil {
		t.Fatal(err)
	}
	return uint64(secs * 1e6)
}

func fakeXrayBinary(t *testing.T, version string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", dir)
	script := "#!/bin/sh\necho \"Xray " + version + " (Xray, Penetrates Everything.)\"\n"
	if err := os.WriteFile(filepath.Join(dir, GetBinaryName()), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptedCoreOlderThanTheInstalledBinaryAsksForARestart(t *testing.T) {
	f := setupFakeUnit(t)
	fakeXrayBinary(t, "26.1.1")
	startGood(t) // records 26.1.1 as the version the core started with
	fakeXrayBinary(t, "26.2.2")

	f.startMono = uptimeMicros(t) - 60e6 // the core started a minute before the new binary
	stale := AdoptRunningUnit()
	if stale == nil {
		t.Fatal("the running unit was not adopted")
	}
	if stale.GetXrayVersion() != "26.1.1" || !strings.Contains(stale.CoreRestartNeeded(), "26.2.2") {
		t.Fatalf("version %q, restart note %q: want the running 26.1.1 and a note naming 26.2.2",
			stale.GetXrayVersion(), stale.CoreRestartNeeded())
	}

	f.startMono = uptimeMicros(t) + 60e6 // started from the new binary
	fresh := AdoptRunningUnit()
	if fresh == nil {
		t.Fatal("the running unit was not adopted")
	}
	if fresh.GetXrayVersion() != "26.2.2" || fresh.CoreRestartNeeded() != "" {
		t.Fatalf("version %q, restart note %q: want 26.2.2 and no note", fresh.GetXrayVersion(), fresh.CoreRestartNeeded())
	}
}

func TestUnitStartRefusesAGeoFileTheCoreCannotRead(t *testing.T) {
	f := setupFakeUnit(t)
	bin := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", bin)
	geoip := filepath.Join(bin, "geoip.dat")
	if err := os.WriteFile(geoip, []byte("geo"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := unitTestConfig("warning")
	cfg.RouterConfig = json_util.RawMessage(`{"rules":[{"type":"field","ip":["geoip:private"],"outboundTag":"blocked"}]}`)

	err := NewProcess(cfg).Start()

	if !errors.Is(err, ErrConfigRejected) || !strings.Contains(err.Error(), geoip) {
		t.Fatalf("Start() = %v, want ErrConfigRejected naming %s", err, geoip)
	}
	if f.count("restart") != 0 {
		t.Fatal("the core was restarted into a config whose geo file it cannot read")
	}
	if err := os.Chmod(geoip, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewProcess(cfg).Start(); err != nil {
		t.Fatalf("Start() with a readable geo file = %v", err)
	}
}

func TestUnitConfigErrorAllowsOnlySocketsTheServiceShares(t *testing.T) {
	setupFakeUnit(t)
	cases := []struct {
		listen string
		ok     bool
	}{
		{`"0.0.0.0"`, true},
		{`"@xray-abstract"`, true},
		{`"/dev/shm/xray.sock,0666"`, true},
		{`"/run/still-xray/h2.sock"`, true},
		{`"/tmp/xray.sock"`, false},
		{`"/var/run/xray.sock"`, false},
		{`"/run/still-xray/../nginx/x.sock"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.listen, func(t *testing.T) {
			cfg := &Config{InboundConfigs: []InboundConfig{{Tag: "fallback", Listen: json_util.RawMessage(tc.listen)}}}
			err := UnitConfigError(cfg)
			if (err == nil) != tc.ok {
				t.Fatalf("UnitConfigError(%s) = %v, want ok=%v", tc.listen, err, tc.ok)
			}
		})
	}
}

// A freedom outbound with sockopt.mark passes `xray run -test` (it runs as root) and then fails
// every connection with "failed to set SO_MARK" under a unit without CAP_NET_ADMIN.
func TestUnitConfigErrorFollowsTheRunningUnitsCapabilities(t *testing.T) {
	f := setupFakeUnit(t)
	marked := unitTestConfig("warning")
	marked.OutboundConfigs = json_util.RawMessage(`[{"tag":"warp","protocol":"freedom","streamSettings":{"sockopt":{"mark":51820}}}]`)
	if err := UnitConfigError(marked); err != nil {
		t.Fatalf("the shipped unit (CAP_NET_ADMIN) refuses a socket mark: %v", err)
	}
	f.ambient = "cap_net_bind_service"
	err := UnitConfigError(marked)
	if err == nil || !strings.Contains(err.Error(), `outbound "warp" (socket mark) needs CAP_NET_ADMIN`) {
		t.Fatalf("a unit without CAP_NET_ADMIN: UnitConfigError = %v, want the socket mark refused", err)
	}
	if err := NewProcess(marked).Start(); !errors.Is(err, ErrConfigRejected) {
		t.Fatalf("Start() under that unit = %v, want ErrConfigRejected before the core restarts", err)
	}
	f.ambient = ""
	tun := unitTestConfig("warning")
	tun.InboundConfigs = append(tun.InboundConfigs, InboundConfig{Tag: "tun0", Protocol: "tun"})
	if err := UnitConfigError(tun); err == nil || !strings.Contains(err.Error(), "/dev/net/tun") {
		t.Fatalf("TUN under the sandboxed unit: UnitConfigError = %v, want it refused", err)
	}
}

// update.sh rewrites the unit with CAP_NET_ADMIN and daemon-reloads, but leaves the core running
// with the old set: a socket mark hot-added to it would fail every connection until a restart.
func TestUnitRestartNeededForACoreStartedWithoutTheUnitsCapabilities(t *testing.T) {
	setupFakeUnit(t)
	status := filepath.Join(t.TempDir(), "status")
	prev := procStatusFile
	procStatusFile = func(int) string { return status }
	t.Cleanup(func() { procStatusFile = prev })
	marked := unitTestConfig("warning")
	marked.OutboundConfigs = json_util.RawMessage(`[{"tag":"warp","protocol":"freedom","streamSettings":{"sockopt":{"mark":51820}}}]`)

	writeCaps := func(capEff string) {
		t.Helper()
		if err := os.WriteFile(status, []byte("Name:\txray\nCapInh:\t0000000000000000\nCapEff:\t"+capEff+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeCaps("0000000000000400") // CAP_NET_BIND_SERVICE only: started under the old unit
	if !UnitRestartNeeded(marked) {
		t.Fatal("a core without CAP_NET_ADMIN would get the socket mark through the API")
	}
	if err := UnitConfigError(marked); err != nil {
		t.Fatalf("UnitConfigError = %v: the unit grants CAP_NET_ADMIN, a restart serves the config", err)
	}
	if UnitRestartNeeded(unitTestConfig("warning")) {
		t.Fatal("a config without marks restarts the core")
	}
	writeCaps("0000000000001400")
	if UnitRestartNeeded(marked) {
		t.Fatal("a core that has CAP_NET_ADMIN is restarted for a socket mark")
	}
}

func TestUnitStartRefusesASocketInThePrivateTmp(t *testing.T) {
	f := setupFakeUnit(t)
	cfg := unitTestConfig("warning")
	cfg.InboundConfigs = append(cfg.InboundConfigs, InboundConfig{
		Tag: "fallback", Protocol: "vless", Listen: json_util.RawMessage(`"/tmp/xray.sock"`),
	})

	err := NewProcess(cfg).Start()

	if !errors.Is(err, ErrConfigRejected) || !strings.Contains(err.Error(), "/tmp/xray.sock") {
		t.Fatalf("Start() = %v, want ErrConfigRejected naming the socket", err)
	}
	if f.count("restart") != 0 {
		t.Fatal("the core was restarted into a config whose socket nobody can reach")
	}
}

func TestReadoptRetriesWhileTheRestartedCoreIsActivating(t *testing.T) {
	f := setupFakeUnit(t)
	startGood(t)
	TakeUnitRestart()

	f.active = "activating"
	if ReadoptRestartedUnit() != nil || !TakeUnitRestart() {
		t.Fatal("a core still activating was neither adopted nor left for the next tick")
	}
	f.active = "active"
	if ReadoptRestartedUnit() == nil || TakeUnitRestart() {
		t.Fatal("the core that came up was not adopted once")
	}
}

// The panel runs with UMask=0077 and RestrictSUIDSGID: a folder it creates must come out 0750
// with the setgid bit of install.sh's /etc/still-xray, which a chmod would clear for good.
func TestUnitFoldersStayReadableByTheCoreGroupUnderThePanelUmask(t *testing.T) {
	setupFakeUnit(t)
	folder := filepath.Join(t.TempDir(), "still-xray")
	if err := os.Mkdir(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(folder, 0o750|os.ModeSetgid); err != nil { // as install.sh leaves it
		t.Fatal(err)
	}
	t.Setenv("XUI_XRAY_UNIT_FOLDER", folder)
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "a.key"), "key")
	writeTestFile(t, filepath.Join(src, "a.pem"), "cert")
	cfg := unitTestConfig("warning")
	cfg.InboundConfigs = append(cfg.InboundConfigs, tlsInbound("a", src))

	previous := syscall.Umask(0o077)
	MirrorCertificateFiles(cfg)
	err := NewProcess(cfg).Start()
	if during := syscall.Umask(previous); during != 0o077 {
		t.Fatalf("creating the folders changed the process umask to %o", during)
	}
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{folder, certMirrorFolder()} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o750 || info.Mode()&os.ModeSetgid == 0 {
			t.Fatalf("%s mode %v, want drwxr-s--- for the core's group", dir, info.Mode())
		}
	}
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func tlsInbound(name, dir string) InboundConfig {
	stream, _ := json.Marshal(map[string]any{"security": "tls", "tlsSettings": map[string]any{
		"certificates": []any{map[string]any{
			"certificateFile": filepath.Join(dir, name+".pem"), "keyFile": filepath.Join(dir, name+".key"),
		}},
	}})
	return InboundConfig{
		Tag: "tls-" + name, Protocol: "vless", Port: 443, Listen: json_util.RawMessage(`"0.0.0.0"`),
		StreamSettings: json_util.RawMessage(stream),
	}
}

func mirrorPaths(t *testing.T, ib InboundConfig) (cert, key string) {
	t.Helper()
	var stream struct {
		TLSSettings struct {
			Certificates []struct{ CertificateFile, KeyFile string } `json:"certificates"`
		} `json:"tlsSettings"`
	}
	if err := json.Unmarshal(ib.StreamSettings, &stream); err != nil {
		t.Fatal(err)
	}
	c := stream.TLSSettings.Certificates[0]
	return c.CertificateFile, c.KeyFile
}

func TestStartRemovesCertificateCopiesNoLongerInTheConfig(t *testing.T) {
	setupFakeUnit(t)
	src := t.TempDir()
	for _, name := range []string{"a", "b"} {
		writeTestFile(t, filepath.Join(src, name+".key"), name+" key")
		writeTestFile(t, filepath.Join(src, name+".pem"), name+" cert")
	}
	both := unitTestConfig("warning")
	both.InboundConfigs = append(both.InboundConfigs, tlsInbound("a", src), tlsInbound("b", src))
	MirrorCertificateFiles(both)
	if err := NewProcess(both).Start(); err != nil {
		t.Fatal(err)
	}
	bCert, bKey := mirrorPaths(t, both.InboundConfigs[2])

	onlyA := unitTestConfig("warning")
	onlyA.InboundConfigs = append(onlyA.InboundConfigs, tlsInbound("a", src))
	MirrorCertificateFiles(onlyA)
	if err := NewProcess(onlyA).Start(); err != nil {
		t.Fatal(err)
	}

	aCert, aKey := mirrorPaths(t, onlyA.InboundConfigs[1])
	for _, kept := range []string{aCert, aKey} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("copy %s the running config uses: %v", kept, err)
		}
	}
	for _, gone := range []string{bCert, bKey} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("copy %s of a removed inbound still on disk (err=%v)", gone, err)
		}
	}
	if CertificateSource(bKey) != bKey {
		t.Fatal("a removed copy is still refreshed from its source")
	}
}

func TestCertificateCopyNeverMovesAheadOfItsKey(t *testing.T) {
	setupFakeUnit(t)
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 key anyway")
	}
	src := t.TempDir()
	keySource, certSource := filepath.Join(src, "a.key"), filepath.Join(src, "a.pem")
	writeTestFile(t, keySource, "old key")
	writeTestFile(t, certSource, "old cert")
	cfg := unitTestConfig("warning")
	cfg.InboundConfigs = append(cfg.InboundConfigs, tlsInbound("a", src))
	MirrorCertificateFiles(cfg)
	certMirror, keyMirror := mirrorPaths(t, cfg.InboundConfigs[1])

	writeTestFile(t, keySource, "new key")
	writeTestFile(t, certSource, "new cert")
	if err := os.Chmod(keySource, 0); err != nil {
		t.Fatal(err)
	}
	SyncCertificateMirrors()
	if cert, _ := os.ReadFile(certMirror); string(cert) != "old cert" {
		t.Fatalf("certificate copy %q moved ahead of a key copy that could not be refreshed", cert)
	}

	if err := os.Chmod(keySource, 0o600); err != nil {
		t.Fatal(err)
	}
	changed := SyncCertificateMirrors()
	cert, _ := os.ReadFile(certMirror)
	key, _ := os.ReadFile(keyMirror)
	if string(cert) != "new cert" || string(key) != "new key" || len(changed) != 2 || changed[0] != keySource {
		t.Fatalf("after the key is readable: cert %q key %q changed %v, want both new, key first", cert, key, changed)
	}
}
