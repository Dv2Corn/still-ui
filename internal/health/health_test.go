package health

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func phases(ns []Notice) []Phase {
	var out []Phase
	for _, n := range ns {
		out = append(out, n.Phase)
	}
	return out
}

func TestTrackerAlertsOncePerStateChange(t *testing.T) {
	tr := NewTracker()
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	bad := func(pct string) []Check {
		return []Check{{Key: "disk:/", Event: "disk.high", Problem: true, Params: map[string]string{"Percent": pct}}}
	}
	ok := []Check{{Key: "disk:/", Event: "disk.high", Params: map[string]string{"Percent": "40"}}}
	remind := 6 * time.Hour

	got := tr.Observe(t0, remind, bad("95"))
	if len(got) != 1 || got[0].Phase != Raised || got[0].Params["Percent"] != "95" || !got[0].Since.Equal(t0) {
		t.Fatalf("first bad round = %+v, want one raised alert", got)
	}
	for i := 1; i < 60; i++ {
		if got := tr.Observe(t0.Add(time.Duration(i)*time.Minute), remind, bad("96")); len(got) != 0 {
			t.Fatalf("minute %d repeated the alert: %+v", i, got)
		}
	}
	got = tr.Observe(t0.Add(remind), remind, bad("97"))
	if len(got) != 1 || got[0].Phase != Reminder || !got[0].Since.Equal(t0) {
		t.Fatalf("after %v = %+v, want one reminder since the start", remind, got)
	}
	if got := tr.Observe(t0.Add(remind+time.Minute), remind, bad("97")); len(got) != 0 {
		t.Fatalf("the reminder repeated at once: %+v", got)
	}
	got = tr.Observe(t0.Add(remind+2*time.Minute), remind, ok)
	if len(got) != 1 || got[0].Phase != Resolved || got[0].Params["Percent"] != "40" {
		t.Fatalf("fixed = %+v, want one resolved alert with the current value", got)
	}
	if got := tr.Observe(t0.Add(remind+3*time.Minute), remind, ok); len(got) != 0 {
		t.Fatalf("resolved twice: %+v", got)
	}
}

func TestTrackerNoReminderWhenDisabled(t *testing.T) {
	tr := NewTracker()
	t0 := time.Now()
	c := []Check{{Key: "k", Event: "e", Problem: true}}
	tr.Observe(t0, 0, c)
	if got := tr.Observe(t0.Add(1000*time.Hour), 0, c); len(got) != 0 {
		t.Fatalf("remind=0 still reminded: %+v", got)
	}
}

func TestTrackerConfirmIgnoresShortBlips(t *testing.T) {
	tr := NewTracker()
	t0 := time.Now()
	bad := []Check{{Key: "xray.down", Event: "xray.down", Problem: true, Confirm: 2}}
	good := []Check{{Key: "xray.down", Event: "xray.down"}}
	if got := tr.Observe(t0, time.Hour, bad); len(got) != 0 {
		t.Fatalf("one bad observation raised: %+v", got)
	}
	// systemd brought the core back: no alert, and no "resolved" for an alert never sent.
	if got := tr.Observe(t0.Add(time.Minute), time.Hour, good); len(got) != 0 {
		t.Fatalf("a blip produced %v", phases(got))
	}
	tr.Observe(t0.Add(2*time.Minute), time.Hour, bad)
	got := tr.Observe(t0.Add(3*time.Minute), time.Hour, bad)
	if len(got) != 1 || got[0].Phase != Raised || !got[0].Since.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("two bad observations = %+v, want raised since the first of them", got)
	}
}

// A memory figure that lives around the threshold must not raise and resolve every few minutes:
// it resolves only after Settle good rounds, and a value in the hold band changes nothing.
func TestTrackerHysteresis(t *testing.T) {
	tr := NewTracker()
	t0 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	round := 0
	observe := func(problem, hold bool) []Phase {
		round++
		c := Check{Key: "mem:Xray", Event: "process.memory", Problem: problem, Hold: hold, Confirm: 3, Settle: 3}
		return phases(tr.Observe(t0.Add(time.Duration(round)*time.Minute), 6*time.Hour, []Check{c}))
	}
	// 91, 86 (hold), 91, 91: the hold band breaks the streak, so three bad rounds in a row are needed.
	for i, step := range []struct{ problem, hold bool }{{true, false}, {false, true}, {true, false}, {true, false}} {
		if got := observe(step.problem, step.hold); len(got) != 0 {
			t.Fatalf("round %d before a sustained problem = %v", i, got)
		}
	}
	if got := observe(true, false); len(got) != 1 || got[0] != Raised {
		t.Fatalf("third bad round in a row = %v, want raised", got)
	}
	// Back and forth inside the band and once below it: still raised, nothing sent.
	for i, step := range []struct{ problem, hold bool }{{false, true}, {false, false}, {false, true}, {false, false}, {false, false}, {true, false}} {
		if got := observe(step.problem, step.hold); len(got) != 0 {
			t.Fatalf("flapping round %d = %v, want silence", i, got)
		}
	}
	observe(false, false)
	observe(false, false)
	if got := observe(false, false); len(got) != 1 || got[0] != Resolved {
		t.Fatalf("third good round in a row = %v, want resolved", got)
	}
}

func TestTrackerForgetsVanishedConditions(t *testing.T) {
	tr := NewTracker()
	t0 := time.Now()
	tr.Observe(t0, time.Hour, []Check{{Key: "cert:inbound:a", Event: "cert.expiring", Problem: true}})
	// The inbound was deleted: no "resolved", and a new one with the same tag starts fresh.
	if got := tr.Observe(t0.Add(time.Minute), time.Hour, nil); len(got) != 0 {
		t.Fatalf("vanished condition produced %v", phases(got))
	}
	got := tr.Observe(t0.Add(2*time.Minute), time.Hour, []Check{{Key: "cert:inbound:a", Event: "cert.expiring", Problem: true}})
	if len(got) != 1 || got[0].Phase != Raised {
		t.Fatalf("returning condition = %v, want raised", phases(got))
	}
}

func TestTrackerKeepsConditionsApart(t *testing.T) {
	tr := NewTracker()
	t0 := time.Now()
	got := tr.Observe(t0, time.Hour, []Check{
		{Key: "mem:Xray", Event: "process.memory", Problem: true},
		{Key: "mem:still-ui", Event: "process.memory", Problem: true},
		{Key: "disk:/", Event: "disk.high"},
	})
	if len(got) != 2 || got[0].Key != "mem:Xray" || got[1].Key != "mem:still-ui" {
		t.Fatalf("got %+v, want two raised alerts in key order", got)
	}
}

func TestEventWindow(t *testing.T) {
	w := NewEventWindow(time.Hour)
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	for _, m := range []int{0, 3, 8, 9} {
		w.Add(t0.Add(time.Duration(m) * time.Minute))
	}
	if n := w.Count(t0.Add(10*time.Minute), 10*time.Minute); n != 4 {
		t.Fatalf("count in 10 min = %d, want 4", n)
	}
	if n := w.Count(t0.Add(12*time.Minute), 10*time.Minute); n != 3 {
		t.Fatalf("count after the first left the window = %d, want 3", n)
	}
	if n := w.Count(t0.Add(3*time.Hour), 10*time.Minute); n != 0 {
		t.Fatalf("old events still counted: %d", n)
	}
	if len(w.times) != 0 {
		t.Fatalf("events older than keep are retained: %d", len(w.times))
	}
}

func selfSigned(t *testing.T, notBefore, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "cn.example"},
		DNSNames: []string{"vpn.example.com"}, NotBefore: notBefore, NotAfter: notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	// A key block first: a combined file must still yield the certificate.
	return append(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
}

func TestParseCertificateAndExpiry(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	cert, err := ParseCertificate(selfSigned(t, now.Add(-80*24*time.Hour), now.Add(10*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if cert.DNSNames[0] != "vpn.example.com" || !cert.NotAfter.Equal(now.Add(10*24*time.Hour)) {
		t.Fatalf("parsed %v %v", cert.DNSNames, cert.NotAfter)
	}
	if _, err := ParseCertificate([]byte("not a pem")); err == nil {
		t.Fatal("garbage parsed")
	}

	day := 24 * time.Hour
	cases := []struct {
		name          string
		before, after time.Duration // relative to now
		days          int
		want          bool
	}{
		{"90-day cert, 10 days left, warn at 7", -80 * day, 10 * day, 7, false},
		{"90-day cert, 6 days left, warn at 7", -84 * day, 6 * day, 7, true},
		{"expired", -90 * day, -time.Hour, 7, true},
		// A 6-day IP certificate renewed with 2 days left must not alert all its life.
		{"6-day cert, 3 days left", -3 * day, 3 * day, 7, false},
		{"6-day cert, 1 day left", -5 * day, 1 * day, 7, true},
		{"30 days asked, 20 left of 90", -70 * day, 20 * day, 30, true},
	}
	for _, c := range cases {
		if got := CertExpiring(now.Add(c.before), now.Add(c.after), now, c.days); got != c.want {
			t.Errorf("%s: CertExpiring = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCgroupAndMemoryParsing(t *testing.T) {
	path, ok := CgroupPath("12:pids:/legacy\n0::/system.slice/still-xray.service\n")
	if !ok || path != "/system.slice/still-xray.service" {
		t.Fatalf("CgroupPath = %q, %v", path, ok)
	}
	if _, ok := CgroupPath("1:name=systemd:/x\n"); ok {
		t.Fatal("cgroup v1 only must report no v2 path")
	}
	if ParseMemoryMax("max\n") != 0 || ParseMemoryMax("536870912\n") != 536870912 || ParseMemoryMax("") != 0 {
		t.Fatal("ParseMemoryMax")
	}
	stat := "anon 300\nfile 700\nactive_file 200\ninactive_file 500\n"
	if got := WorkingSet(1000, stat); got != 500 {
		t.Fatalf("WorkingSet = %d, want 500", got)
	}
	if got := WorkingSet(100, stat); got != 100 {
		t.Fatalf("WorkingSet with inactive_file > current = %d, want current", got)
	}
	if Percent(50, 200) != 25 || Percent(1, 0) != 0 {
		t.Fatal("Percent")
	}
}
