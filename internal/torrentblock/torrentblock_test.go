package torrentblock

import (
	"errors"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/nfttable"
	"github.com/mhsanaei/3x-ui/v3/internal/nfttable/nfttest"
)

func TestApplyRefusesWhereNftCannotRun(t *testing.T) {
	cases := []struct {
		name string
		goos string
		r    *nfttest.Runner
		want error
	}{
		{"windows", "windows", &nfttest.Runner{}, nfttable.ErrUnsupported},
		{"no nft binary", "linux", &nfttest.Runner{NoNft: true}, nfttable.ErrNftMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newManager(tc.r, tc.goos)
			if err := m.Apply(nil); !errors.Is(err, tc.want) {
				t.Fatalf("Apply() = %v, want %v", err, tc.want)
			}
			if err := m.Ensure(nil); !errors.Is(err, tc.want) {
				t.Fatalf("Ensure() = %v, want %v", err, tc.want)
			}
			if len(tc.r.Loads) != 0 {
				t.Fatal("ruleset was sent to nft on a host that cannot run it")
			}
			if err := m.Remove(); err != nil {
				t.Fatalf("Remove() = %v, want nil: nothing can be loaded there", err)
			}
		})
	}
}

func TestApplyReportsNftOutput(t *testing.T) {
	m := newManager(&nfttest.Runner{ApplyErr: errors.New("exit status 1")}, "linux")
	err := m.Apply(nil)
	if err == nil || !strings.Contains(err.Error(), "Error: syntax error") {
		t.Fatalf("Apply() = %v, want the nft message in the error", err)
	}
}

// A periodic Ensure must not reload a live table: reloading forgets every learned peer.
// It only swaps the protected ports, which change as inbounds come and go.
func TestEnsureLoadsOnlyAMissingTable(t *testing.T) {
	r := &nfttest.Runner{}
	m := newManager(r, "linux")
	if err := m.Ensure([]uint16{2053}); err != nil || len(r.Loads) != 1 {
		t.Fatalf("Ensure on a bare host: err %v, applied %d, want one load", err, len(r.Loads))
	}
	if err := m.Ensure([]uint16{443, 2053, 0, 2053}); err != nil || len(r.Loads) != 1 {
		t.Fatalf("Ensure with the table loaded: err %v, applied %d, want no reload", err, len(r.Loads))
	}
	want := "flush set inet still_torrent_block protected\nadd element inet still_torrent_block protected { 22, 443, 2053 }\n"
	if r.Refresh != want {
		t.Fatalf("protected refresh = %q, want %q (SSH always kept, zero and duplicates dropped)", r.Refresh, want)
	}
	r.TableUp = false
	if err := m.Ensure(nil); err != nil || len(r.Loads) != 2 {
		t.Fatalf("Ensure after the table vanished: err %v, applied %d, want a reload", err, len(r.Loads))
	}
}

func TestStatusReadsCountersOfOwnTable(t *testing.T) {
	r := &nfttest.Runner{TableUp: true, Counters: `{"nftables":[{"metainfo":{"version":"1.0.6"}},
{"counter":{"family":"inet","name":"sig_bt","table":"still_torrent_block","handle":1,"packets":3,"bytes":204}},
{"counter":{"family":"inet","name":"sig_dht","table":"still_torrent_block","handle":2,"packets":61,"bytes":6100}},
{"counter":{"family":"inet","name":"sig_tracker","table":"still_torrent_block","handle":3,"packets":2,"bytes":56}},
{"counter":{"family":"inet","name":"sig_utp","table":"still_torrent_block","handle":4,"packets":276,"bytes":7728}},
{"counter":{"family":"inet","name":"learned","table":"still_torrent_block","handle":7,"packets":5,"bytes":1}},
{"counter":{"family":"inet","name":"peer_hit","table":"still_torrent_block","handle":5,"packets":59811,"bytes":1}},
{"counter":{"family":"inet","name":"ban_hit","table":"still_torrent_block","handle":6,"packets":7,"bytes":1}},
{"counter":{"family":"inet","name":"sig_bt","table":"someone_else","handle":1,"packets":999,"bytes":1}}]}`}
	st := newManager(r, "linux").Status()
	want := Status{Supported: true, NftInstalled: true, Active: true, Counters: Counters{
		Handshakes: 3, DHT: 61, Trackers: 2, UTP: 276, Learned: 5, PeerDrops: 59811, TunnelBans: 7,
	}}
	if st != want {
		t.Fatalf("Status() = %+v, want %+v", st, want)
	}
}

func TestStatusWithoutTable(t *testing.T) {
	cases := []struct {
		name string
		goos string
		r    *nfttest.Runner
		want Status
	}{
		{"not loaded", "linux", &nfttest.Runner{}, Status{Supported: true, NftInstalled: true}},
		{"no nft", "linux", &nfttest.Runner{NoNft: true}, Status{Supported: true}},
		{"not linux", "darwin", &nfttest.Runner{TableUp: true}, Status{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newManager(tc.r, tc.goos).Status(); got != tc.want {
				t.Fatalf("Status() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
