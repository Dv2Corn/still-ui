package egressfilter

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	noNft    bool
	tableUp  bool
	counters string
	applyErr error
	loads    []string
	removed  int
}

func (f *fakeRunner) lookPath(string) error {
	if f.noNft {
		return errors.New("not found")
	}
	return nil
}

func (f *fakeRunner) run(stdin string, args ...string) (string, error) {
	switch {
	case strings.Join(args, " ") == "-f -" && strings.Contains(stdin, "table inet "+TableName+" {"):
		if f.applyErr != nil {
			return "Error: syntax error", f.applyErr
		}
		f.loads = append(f.loads, stdin)
		f.tableUp = true
	case strings.Join(args, " ") == "-f -":
		f.removed++
		f.tableUp = false
	case args[0] == "list" || args[0] == "-j":
		if !f.tableUp {
			return "Error: No such file or directory", errors.New("exit status 1")
		}
		if args[0] == "-j" {
			return f.counters, nil
		}
	}
	return "", nil
}

func TestParseCustomCanonicalizes(t *testing.T) {
	c, err := ParseCustom([]string{
		"203.0.113.77/24, 203.0.113.5", // the single address is inside the /24
		"2001:db8::1\n2001:db8::/32",
		"::ffff:198.51.100.7",
		"6667 1000-2000;1500 2001",
		"6667",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"198.51.100.7", "203.0.113.0/24", "2001:db8::/32", "1000-2001", "6667"}
	if got := c.Strings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Strings() = %v, want %v", got, want)
	}
	again, err := ParseCustom(c.Strings())
	if err != nil || !reflect.DeepEqual(again.Strings(), want) {
		t.Fatalf("canonical form is not stable: %v %v", again.Strings(), err)
	}
}

func TestParseCustomRefuses(t *testing.T) {
	for _, tok := range []string{"53", "1-1024", "0", "70000", "2000-1000", "0.0.0.0/0", "::/0", "example.com", "geoip:ru", "10.0.0.0/33", "fe80::1%eth0"} {
		if _, err := ParseCustom([]string{tok}); err == nil {
			t.Errorf("%q accepted", tok)
		}
	}
	many := make([]string, MaxCustomEntries+1)
	for i := range many {
		many[i] = "1000"
	}
	if _, err := ParseCustom(many); err == nil {
		t.Error("an oversized list was accepted")
	}
}

func TestFingerprintFollowsNormalizedFilter(t *testing.T) {
	a, _ := Filter{SMTP: true, Custom: []string{"203.0.113.9/24"}}.Normalize()
	b, _ := Filter{SMTP: true, Custom: []string{"203.0.113.0/24", "203.0.113.1"}}.Normalize()
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("equal filters differ: %v vs %v", a, b)
	}
	c, _ := Filter{SMTP: true, MailSubmission: true}.Normalize()
	if a.Fingerprint() == c.Fingerprint() {
		t.Fatal("different filters share a fingerprint")
	}
}

func TestRulesetOnlyJudgesTunnelClients(t *testing.T) {
	tx, err := Ruleset(Spec{
		Filter:      Filter{SMTP: true, MailSubmission: true, Private: true, Server: true, Custom: []string{"203.0.113.0/24", "2001:db8::/32", "6667"}},
		ServerPorts: []uint16{2053, 22, 22, 53, 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"set mail_ports { type inet_service; flags interval; elements = { 25, 465, 587 }; }",
		"set server_ports { type inet_service; elements = { 22, 2053 }; }",
		"set custom4 { type ipv4_addr; flags interval; elements = { 203.0.113.0/24 }; }",
		"set custom6 { type ipv6_addr; flags interval; elements = { 2001:db8::/32 }; }",
		"set custom_ports { type inet_service; flags interval; elements = { 6667 }; }",
		`oifname != "xui-awg*" ip daddr @private4 counter name private goto deny`,
		"type filter hook forward priority 0; policy accept;",
		`iifname "xui-awg*" ct direction original jump client`,
		`iifname "xui-awg*" ct direction original meta l4proto tcp tcp dport @server_ports counter name server goto deny`,
		"meta l4proto tcp reject with tcp reset",
	} {
		if !strings.Contains(tx, want) {
			t.Errorf("ruleset lacks %q:\n%s", want, tx)
		}
	}
	// Name resolution is let through before any block, even to a private resolver.
	dns := strings.Index(tx, "th dport 53 return")
	if dns < 0 || dns > strings.Index(tx, "@mail_ports counter") || dns > strings.Index(tx, "@private4 counter") {
		t.Errorf("DNS is not let through ahead of the blocks:\n%s", tx)
	}
	// The server's own traffic is never judged: no output hook, no rule outside the tunnel.
	if strings.Contains(tx, "hook output") {
		t.Fatalf("ruleset hooks output:\n%s", tx)
	}
	for _, line := range strings.Split(tx, "\n") {
		if strings.Contains(line, "hook") {
			continue
		}
		if strings.Contains(line, "jump client") || strings.Contains(line, "@server_ports counter") {
			if !strings.Contains(line, `iifname "xui-awg*" ct direction original`) {
				t.Errorf("entry rule not limited to tunnel clients: %q", line)
			}
		}
	}
}

func TestRulesetLeavesOutWhatIsOff(t *testing.T) {
	tx, err := Ruleset(Spec{Filter: Filter{SMTP: true}, ServerPorts: []uint16{2053}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tx, "elements = { 25 }") {
		t.Fatalf("SMTP only should block 25:\n%s", tx)
	}
	for _, absent := range []string{"465", "private4", "server_ports", "hook input", "custom4", "custom_ports"} {
		if strings.Contains(tx, absent) {
			t.Errorf("SMTP-only ruleset mentions %q:\n%s", absent, tx)
		}
	}
	if (Spec{Filter: Filter{Server: true}}).Needed() {
		t.Fatal("server block without ports needs no table")
	}
	if !(Spec{Filter: Filter{Server: true}, ServerPorts: []uint16{22}}).Needed() {
		t.Fatal("server block with ports needs the table")
	}
	if _, err := Ruleset(Spec{Filter: Filter{Custom: []string{"53"}}}); err == nil {
		t.Fatal("an invalid custom list rendered")
	}
}

func TestManagerRefusesWhereNftCannotRun(t *testing.T) {
	spec := Spec{Filter: Filter{SMTP: true}}
	if err := (&Manager{r: &fakeRunner{}, goos: "darwin"}).Apply(spec); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("darwin: %v", err)
	}
	if err := (&Manager{r: &fakeRunner{noNft: true}, goos: "linux"}).Apply(spec); !errors.Is(err, ErrNftMissing) {
		t.Fatalf("no nft: %v", err)
	}
	if err := (&Manager{r: &fakeRunner{noNft: true}, goos: "linux"}).Remove(); err != nil {
		t.Fatalf("Remove without nft: %v", err)
	}
}

func TestManagerEnsureReloadsOnlyWhenNeeded(t *testing.T) {
	r := &fakeRunner{}
	m := &Manager{r: r, goos: "linux"}
	spec := Spec{Filter: Filter{SMTP: true}}
	if err := m.Ensure(spec); err != nil || len(r.loads) != 1 {
		t.Fatalf("first Ensure: loads %d, %v", len(r.loads), err)
	}
	if err := m.Ensure(spec); err != nil || len(r.loads) != 1 {
		t.Fatalf("unchanged Ensure reloaded: loads %d, %v", len(r.loads), err)
	}
	r.tableUp = false // a foreign "nft flush ruleset"
	if err := m.Ensure(spec); err != nil || len(r.loads) != 2 {
		t.Fatalf("Ensure after a flush: loads %d, %v", len(r.loads), err)
	}
	spec.Filter.MailSubmission = true
	if err := m.Ensure(spec); err != nil || len(r.loads) != 3 || !strings.Contains(r.loads[2], "587") {
		t.Fatalf("Ensure after a change: loads %d, %v", len(r.loads), err)
	}
	if err := m.Ensure(Spec{}); err != nil || r.tableUp {
		t.Fatalf("Ensure with nothing to block kept the table: %v", err)
	}
	if err := m.Apply(Spec{}); err != nil {
		t.Fatalf("Apply of an empty spec: %v", err)
	}
}

func TestManagerApplyFailureIsReported(t *testing.T) {
	r := &fakeRunner{applyErr: errors.New("exit status 1")}
	m := &Manager{r: r, goos: "linux"}
	err := m.Apply(Spec{Filter: Filter{Private: true}})
	if err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("Apply error = %v", err)
	}
	r.applyErr = nil
	if err := m.Ensure(Spec{Filter: Filter{Private: true}}); err != nil || len(r.loads) != 1 {
		t.Fatalf("Ensure after a failed Apply did not retry: %v", err)
	}
}

func TestStatusCounters(t *testing.T) {
	r := &fakeRunner{tableUp: true, counters: `{"nftables":[{"metainfo":{}},
		{"counter":{"family":"inet","name":"mail","table":"` + TableName + `","packets":7,"bytes":420}},
		{"counter":{"family":"inet","name":"private","table":"` + TableName + `","packets":3}},
		{"counter":{"family":"inet","name":"server","table":"` + TableName + `","packets":2}},
		{"counter":{"family":"inet","name":"custom","table":"` + TableName + `","packets":1}},
		{"counter":{"family":"inet","name":"mail","table":"other","packets":99}}]}`}
	st := (&Manager{r: r, goos: "linux"}).Status()
	want := Status{Supported: true, NftInstalled: true, Active: true, Counters: Counters{Mail: 7, Private: 3, Server: 2, Custom: 1}}
	if st != want {
		t.Fatalf("Status = %+v, want %+v", st, want)
	}
	r.tableUp = false
	if st := (&Manager{r: r, goos: "linux"}).Status(); st.Active {
		t.Fatal("Active without a table")
	}
}
