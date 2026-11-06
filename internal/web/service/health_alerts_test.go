package service

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/certreload"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/health"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCollectMemoryUsesMemoryMaxOrRAM(t *testing.T) {
	root := t.TempDir()
	procRoot, cgroupRoot = filepath.Join(root, "proc"), filepath.Join(root, "cg")
	t.Cleanup(func() { procRoot, cgroupRoot = "/proc", "/sys/fs/cgroup" })
	writeTree(t, root, map[string]string{
		// The panel runs under a 1 GiB MemoryMax; 300 MiB of its 900 MiB is reclaimable cache.
		"proc/10/cgroup": "0::/system.slice/still-ui.service\n",
		"cg/system.slice/still-ui.service/memory.max":     "1073741824\n",
		"cg/system.slice/still-ui.service/memory.current": "943718400\n",
		"cg/system.slice/still-ui.service/memory.stat":    "anon 600\ninactive_file 314572800\n",
		// Xray in its own unit without a cap: its RSS against the machine's RAM.
		"proc/20/cgroup": "0::/system.slice/still-xray.service\n",
		"cg/system.slice/still-xray.service/memory.max": "max\n",
		"proc/20/status": "Name:\txray\nVmRSS:\t  204800 kB\n",
	})
	got := collectMemory([]memProc{{"still-ui", 10}, {"Xray", 20}}, 4<<30)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].name != "still-ui" || got[0].limitKind != "MemoryMax" || got[0].limit != 1<<30 || got[0].used != 600<<20 {
		t.Errorf("panel = %+v, want 600 MiB of a 1 GiB MemoryMax", got[0])
	}
	if got[1].name != "Xray" || got[1].limitKind != "RAM" || got[1].used != 200<<20 || got[1].limit != 4<<30 {
		t.Errorf("xray = %+v, want 200 MiB of 4 GiB RAM", got[1])
	}

	// Xray as the panel's child shares its cgroup: one figure for both, not the same one twice.
	writeTree(t, root, map[string]string{"proc/30/cgroup": "0::/system.slice/still-ui.service\n"})
	got = collectMemory([]memProc{{"still-ui", 10}, {"Xray", 30}}, 4<<30)
	if len(got) != 1 || got[0].name != "still-ui + Xray" {
		t.Fatalf("child mode = %+v, want one shared figure", got)
	}
}

func TestXrayFlappingCountsCrashesInTheWindow(t *testing.T) {
	saved := xrayCrashes
	xrayCrashes = health.NewEventWindow(24 * time.Hour)
	t.Cleanup(func() { xrayCrashes = saved })
	now := time.Now()
	for _, ago := range []time.Duration{30 * time.Minute, 9 * time.Minute, 5 * time.Minute, time.Minute} {
		RecordXrayCrash(now.Add(-ago))
	}
	th := HealthAlertThresholds{XrayRestarts: 3, XrayRestartWindow: 10}
	var flapping health.Check
	for _, c := range (&HealthAlertService{}).xrayChecks(now, th) {
		if c.Event == string(eventbus.EventXrayFlapping) {
			flapping = c
		}
	}
	if !flapping.Problem || flapping.Params["Count"] != "3" || flapping.Params["Window"] != "10" {
		t.Fatalf("flapping = %+v, want 3 restarts in 10 min flagged", flapping)
	}
	th.XrayRestarts = 4
	for _, c := range (&HealthAlertService{}).xrayChecks(now, th) {
		if c.Event == string(eventbus.EventXrayFlapping) && c.Problem {
			t.Fatal("3 restarts flagged with a threshold of 4")
		}
	}
}

func TestDiskChecksDeduplicateFileSystems(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	t.Setenv("XUI_BACKUP_FOLDER", filepath.Join(dir, "not-yet", "backups"))
	checks := diskChecks(1)
	if len(checks) == 0 {
		t.Fatal("no disk checked")
	}
	seen := map[string]bool{}
	for _, c := range checks {
		if seen[c.Key] {
			t.Fatalf("%s checked twice", c.Key)
		}
		seen[c.Key] = true
		if !c.Problem {
			t.Errorf("%s at %s%% is not over a 1%% threshold", c.Key, c.Params["Percent"])
		}
	}
	if diskChecks(0) != nil {
		t.Fatal("threshold 0 must turn the check off")
	}
}

func expiry(c *x509.Certificate) any {
	if c == nil {
		return nil
	}
	return c.NotAfter
}

func writeKeyPair(t *testing.T, dir string, c tls.Certificate) (string, string) {
	t.Helper()
	keyDER, err := x509.MarshalPKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	writeTree(t, dir, map[string]string{
		"fullchain.pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]})),
		"privkey.pem":   string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	})
	return certFile, keyFile
}

// The alert follows what the panel listener serves: a renewed file it has not taken yet does not
// hide the expiring certificate clients still get, and without a listener the file is read.
func TestListenerCertIsTheServedCertificate(t *testing.T) {
	dir := t.TempDir()
	expiring := selfSigned(t, time.Now().Add(2*time.Hour))
	certFile, keyFile := writeKeyPair(t, dir, expiring)
	pair, err := certreload.Load(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	renewed := selfSigned(t, time.Now().Add(60*24*time.Hour))
	writeKeyPair(t, dir, renewed)

	notAfter := func(c tls.Certificate) time.Time {
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return leaf.NotAfter
	}
	certreload.Register("panel", pair)
	t.Cleanup(func() { certreload.Register("panel", nil) })
	if got := listenerCert("panel", certFile); got == nil || !got.NotAfter.Equal(notAfter(expiring)) {
		t.Fatalf("listenerCert expires %v, want %v: the expiring certificate the listener still serves", expiry(got), notAfter(expiring))
	}
	if got := listenerCert("subscription", certFile); got == nil || !got.NotAfter.Equal(notAfter(renewed)) {
		t.Fatalf("no subscription listener: listenerCert expires %v, want the file's %v", expiry(got), notAfter(renewed))
	}
}
