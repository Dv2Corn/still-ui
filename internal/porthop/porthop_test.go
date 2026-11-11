package porthop

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/nfttable"
	"github.com/mhsanaei/3x-ui/v3/internal/nfttable/nfttest"
)

// manyPorts appends n more single ports, all different.
func manyPorts(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString("," + strconv.Itoa(40000+i))
	}
	return b.String()
}

func TestParseRanges(t *testing.T) {
	ok := []struct {
		in   string
		want []Range
	}{
		{"20000-50000", []Range{{20000, 50000}}},
		{" 20000 - 30000 , 40000 ", []Range{{20000, 30000}, {40000, 40000}}},
		{"1024,65535", []Range{{1024, 1024}, {65535, 65535}}},
	}
	for _, tc := range ok {
		got, err := ParseRanges(tc.in)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("ParseRanges(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	if got, err := ParseRanges(manyPorts(maxRanges)[1:]); err != nil || len(got) != maxRanges {
		t.Errorf("ParseRanges of %d entries = %d entries, %v", maxRanges, len(got), err)
	}
	bad := map[string]string{
		"":                            "has an empty entry",
		"20000,":                      "has an empty entry",
		"abc":                         "is not a port or a low-high range",
		"30000-20000":                 "is not a port or a low-high range",
		"20000-70000":                 "is not a port or a low-high range",
		"20000-30000-40000":           "is not a port or a low-high range",
		"443":                         "starts below 1024",
		"1-65535":                     "starts below 1024",
		"-5":                          "is not a port or a low-high range",
		"20000-30000,25000":           "repeats ports of an earlier entry",
		"2000" + manyPorts(maxRanges): "has more than 32 entries",
	}
	for in, want := range bad {
		if _, err := ParseRanges(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseRanges(%q) error = %v, want one containing %q", in, err, want)
		}
	}
}

func TestRulesetGolden(t *testing.T) {
	got := Ruleset([]Redirect{
		{Ranges: []Range{{30000, 31000}, {40000, 40000}}, To: 8443},
		{Ranges: []Range{{20000, 29999}}, To: 443},
	}, []uint16{2053, 22, 443, 2053, 0})
	want := `add table inet still_port_hop
delete table inet still_port_hop
table inet still_port_hop {
	counter redirected {}
	set protected { type inet_service; elements = { 22, 443, 2053 }; }
	chain prerouting {
		type nat hook prerouting priority dstnat - 10; policy accept;
		fib daddr type != local return
		udp dport @protected return
		udp dport { 20000-29999 } counter name redirected redirect to :443
		udp dport { 30000-31000, 40000 } counter name redirected redirect to :8443
	}
}
`
	if got != want {
		t.Errorf("ruleset:\n%s\nwant:\n%s", got, want)
	}
	if empty := Ruleset([]Redirect{{Ranges: []Range{{20000, 20001}}, To: 443}}, nil); !strings.Contains(empty, "set protected { type inet_service; }\n") {
		t.Errorf("a ruleset without protected ports must still declare a loadable set:\n%s", empty)
	}
}

func testManager(r *nfttest.Runner, goos string) (*Manager, *time.Time) {
	m := newManager(r, goos)
	now := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return now }
	return m, &now
}

var oneRedirect = []Redirect{{Ranges: []Range{{20000, 50000}}, To: 443}}

func TestEnsureLoadsOnceAndReloadsOnlyOnChange(t *testing.T) {
	r := &nfttest.Runner{}
	m, _ := testManager(r, "linux")
	for range 3 {
		if err := m.Ensure(oneRedirect, []uint16{22}); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.Loads) != 1 {
		t.Fatalf("an unchanged redirect was loaded %d times, want 1", len(r.Loads))
	}
	if err := m.Ensure(oneRedirect, []uint16{22, 2053}); err != nil {
		t.Fatal(err)
	}
	if len(r.Loads) != 2 || !strings.Contains(r.Loads[1], "{ 22, 2053 }") {
		t.Fatalf("a new protected port must reload the table, loads: %v", r.Loads)
	}
}

func TestEnsureReloadsATableThatVanished(t *testing.T) {
	r := &nfttest.Runner{}
	m, now := testManager(r, "linux")
	if err := m.Ensure(oneRedirect, nil); err != nil {
		t.Fatal(err)
	}
	r.TableUp = false
	if err := m.Ensure(oneRedirect, nil); err != nil || len(r.Loads) != 1 {
		t.Fatalf("within the check interval nft must not run again: loads %d, err %v", len(r.Loads), err)
	}
	*now = now.Add(presenceCheckEvery)
	if err := m.Ensure(oneRedirect, nil); err != nil || len(r.Loads) != 2 || !r.TableUp {
		t.Fatalf("a flushed table must come back: loads %d, up %v, err %v", len(r.Loads), r.TableUp, err)
	}
}

func TestEnsureRemovesTheTableWhenNoRedirectIsLeft(t *testing.T) {
	r := &nfttest.Runner{}
	m, _ := testManager(r, "linux")
	if err := m.Ensure(oneRedirect, nil); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := m.Ensure(nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if r.TableUp || r.Removed != 1 {
		t.Fatalf("table up %v after %d removal(s), want gone after exactly 1", r.TableUp, r.Removed)
	}
}

// A panel that restarts after the option was turned off still finds the old table.
func TestEnsureClearsALeftoverTableOncePerRun(t *testing.T) {
	r := &nfttest.Runner{TableUp: true}
	m, _ := testManager(r, "linux")
	for range 3 {
		if err := m.Ensure(nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if r.TableUp || r.Removed != 1 {
		t.Fatalf("table up %v after %d removal(s), want gone after exactly 1", r.TableUp, r.Removed)
	}
}

func TestEnsureRefusesHostsThatCannotRedirect(t *testing.T) {
	cases := []struct {
		name string
		r    *nfttest.Runner
		goos string
		want error
	}{
		{"not linux", &nfttest.Runner{}, "windows", nfttable.ErrUnsupported},
		{"nft missing", &nfttest.Runner{NoNft: true}, "linux", nfttable.ErrNftMissing},
		{"no NET_ADMIN", &nfttest.Runner{NoPermission: true}, "linux", ErrNotPermitted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := testManager(tc.r, tc.goos)
			if err := m.Ensure(oneRedirect, nil); !errors.Is(err, tc.want) {
				t.Fatalf("Ensure error = %v, want %v", err, tc.want)
			}
			if err := m.Available(); !errors.Is(err, tc.want) {
				t.Fatalf("Available error = %v, want %v", err, tc.want)
			}
			if len(tc.r.Loads) != 0 {
				t.Fatalf("nothing may be loaded on such a host, got %v", tc.r.Loads)
			}
			if err := m.Ensure(nil, nil); err != nil {
				t.Fatalf("a host with no redirect wanted reports no error, got %v", err)
			}
		})
	}
}

func TestEnsureRemovesTheTableAFailedReloadLeftLoaded(t *testing.T) {
	r := &nfttest.Runner{}
	m, _ := testManager(r, "linux")
	// The start-up pass with no redirect uses up the one look for a leftover table.
	if err := m.Ensure(nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(oneRedirect, nil); err != nil {
		t.Fatal(err)
	}
	r.ApplyErr = errors.New("exit status 1")
	if err := m.Ensure(oneRedirect, []uint16{22}); err == nil {
		t.Fatal("a refused transaction must surface")
	}
	r.ApplyErr = nil
	if err := m.Ensure(nil, nil); err != nil || r.TableUp || r.Removed != 2 {
		t.Fatalf("err %v, table up %v after %d removal(s), want the old table removed", err, r.TableUp, r.Removed)
	}
}

func TestEnsureRetriesAFailedLoad(t *testing.T) {
	r := &nfttest.Runner{ApplyErr: errors.New("exit status 1")}
	m, _ := testManager(r, "linux")
	if err := m.Ensure(oneRedirect, nil); err == nil {
		t.Fatal("a refused transaction must surface")
	}
	r.ApplyErr = nil
	if err := m.Ensure(oneRedirect, nil); err != nil || len(r.Loads) != 1 {
		t.Fatalf("the next tick must load the table: loads %d, err %v", len(r.Loads), err)
	}
}

func TestStatusReadsTheRedirectCounter(t *testing.T) {
	r := &nfttest.Runner{TableUp: true, Counters: `{"nftables":[{"counter":{"name":"redirected","table":"still_port_hop","packets":1204}}]}`}
	m, _ := testManager(r, "linux")
	if got, want := m.Status(), (Status{Supported: true, NftInstalled: true, Permitted: true, Active: true, Packets: 1204}); got != want {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
	m, _ = testManager(&nfttest.Runner{NoPermission: true}, "linux")
	if got, want := m.Status(), (Status{Supported: true, NftInstalled: true}); got != want {
		t.Fatalf("status without NET_ADMIN = %+v, want %+v", got, want)
	}
}
