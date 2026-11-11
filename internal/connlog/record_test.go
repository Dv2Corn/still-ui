package connlog

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func accepted(from, to string) xray.AccessLogLine {
	return xray.AccessLogLine{Time: time.UnixMilli(1735732800000), From: from, Accepted: true, To: to, Inbound: "in", Outbound: "direct", Email: "alice"}
}

func TestRecordFromAppliesTheStoragePolicy(t *testing.T) {
	const v4, v6 = "203.0.113.77:54321", "[2001:db8:aa:bb:cc:dd:ee:ff]:4242"
	tests := []struct {
		name          string
		from, to      string
		policy        Policy
		source        string
		sourcePort    int
		network, host string
		port          int
	}{
		{"full keeps both ends", v4, "tcp:maps.example.co.uk:443", Policy{DestinationFull, SourceFull}, "203.0.113.77", 54321, "tcp", "maps.example.co.uk", 443},
		{"socks source carries its network", "udp:" + v4, "udp:8.8.8.8:53", Policy{DestinationFull, SourceFull}, "203.0.113.77", 54321, "udp", "8.8.8.8", 53},
		{"IPv6 ends", v6, "tcp:[2606:4700::1111]:443", Policy{DestinationFull, SourceFull}, "2001:db8:aa:bb:cc:dd:ee:ff", 4242, "tcp", "2606:4700::1111", 443},
		{"IPv4 masked to its /24, port dropped", v4, "tcp:example.com:443", Policy{DestinationFull, SourceMasked}, "203.0.113.0/24", 0, "tcp", "example.com", 443},
		{"IPv6 masked to its /48", v6, "tcp:example.com:443", Policy{DestinationFull, SourceMasked}, "2001:db8:aa::/48", 0, "tcp", "example.com", 443},
		{"IPv4-mapped source is masked as IPv4", "[::ffff:203.0.113.77]:1", "tcp:example.com:443", Policy{DestinationFull, SourceMasked}, "203.0.113.0/24", 0, "tcp", "example.com", 443},
		{"a source that is no address cannot be masked", "tcp:1.2.*.*:5", "tcp:example.com:443", Policy{DestinationFull, SourceMasked}, "", 0, "tcp", "example.com", 443},
		{"no source", v4, "tcp:example.com:443", Policy{DestinationFull, SourceNone}, "", 0, "tcp", "example.com", 443},
		{"registrable domain only", v4, "tcp:Maps.Example.co.uk:443", Policy{DestinationDomain, SourceFull}, "203.0.113.77", 54321, "tcp", "example.co.uk", 443},
		{"an IP destination has no domain to reduce", v4, "tcp:198.51.100.7:22", Policy{DestinationDomain, SourceFull}, "203.0.113.77", 54321, "tcp", "198.51.100.7", 22},
		{"no destination", v4, "tcp:example.com:443", Policy{DestinationNone, SourceFull}, "203.0.113.77", 54321, "tcp", "", 0},
		{"URL of the http inbound keeps host and port only", v4, "http://Example.com/private/path?token=1", Policy{DestinationFull, SourceFull}, "203.0.113.77", 54321, "tcp", "example.com", 80},
		{"a URL that does not parse still loses its path", v4, "http://Example.com:8080/private%zz?token=1", Policy{DestinationFull, SourceFull}, "203.0.113.77", 54321, "tcp", "example.com", 8080},
		{"an unknown policy value stores nothing", v4, "tcp:example.com:443", Policy{"everything", "everything"}, "", 0, "tcp", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, ok := recordFrom(accepted(tt.from, tt.to), tt.policy)
			want := ConnRecord{Time: 1735732800000, Client: "alice", Inbound: "in", Outbound: "direct", Source: tt.source, SourcePort: tt.sourcePort, Network: tt.network, Host: tt.host, Port: tt.port}
			if !ok || rec != want {
				t.Errorf("got %+v (ok %v)\nwant %+v", rec, ok, want)
			}
		})
	}
}

// The real lines of internal/xray's fixture: ten connections, one refused request.
func TestRecordFromOnRealXrayLines(t *testing.T) {
	raw, err := os.ReadFile("../xray/testdata/access_log_real.txt")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if rec, ok := recordFrom(xray.ParseAccessLogLine(line), Policy{DestinationFull, SourceFull}); ok {
			got = append(got, strings.Join([]string{rec.Client, rec.Inbound, rec.Outbound, rec.Source, rec.Network, rec.Host}, "|"))
		}
	}
	want := []string{
		"|in-socks|direct|127.0.0.1|tcp|127.0.0.1",
		"|in-socks|direct|127.0.0.1|tcp|localhost",
		"|in-socks|direct|127.0.0.1|tcp|::1",
		"|in-socks6|direct|::1|tcp|127.0.0.1",
		"|in-socks|blocked|127.0.0.1|tcp|blocked.test",
		"|in-chain|to-vless|127.0.0.1|tcp|127.0.0.1",
		"alice@example.com|in-vless|direct|127.0.0.1|tcp|127.0.0.1",
		"|in-badchain|to-vless-bad|127.0.0.1|tcp|127.0.0.1",
		"|in-socks|direct|127.0.0.1|udp|127.0.0.1",
		// the line a refused request wrote for itself: only its date, years ahead, gives it away to the collector
		"victim|in-y|out-y|203.0.113.9|tcp|forged.test",
		"|in-http|direct|127.0.0.1|tcp|127.0.0.1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("records:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRecordFromSkipsWhatIsNotAClientConnection(t *testing.T) {
	api := accepted("127.0.0.1:1", "tcp:127.0.0.1:62789")
	api.Inbound, api.Outbound = "api", "api"
	refused := accepted("1.2.3.4:5", "")
	refused.Accepted = false
	for name, line := range map[string]xray.AccessLogLine{"the panel's own API call": api, "a refused request": refused} {
		if rec, ok := recordFrom(line, Policy{DestinationFull, SourceFull}); ok {
			t.Errorf("%s was recorded: %+v", name, rec)
		}
	}
}
