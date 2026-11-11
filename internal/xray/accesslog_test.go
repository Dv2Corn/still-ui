package xray

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// The lines were written by the Xray the panel ships: see TestAccessLog_E2E.
func TestParseAccessLogLineOnRealLines(t *testing.T) {
	raw, err := os.ReadFile(accessLogFixture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	want := []AccessLogLine{
		{From: "tcp:127.0.0.1:46638", Accepted: true, To: "tcp:127.0.0.1:40877", Inbound: "in-socks", Outbound: "direct"},
		{From: "tcp:127.0.0.1:46648", Accepted: true, To: "tcp:localhost:40877", Inbound: "in-socks", Outbound: "direct"},
		{From: "tcp:127.0.0.1:46656", Accepted: true, To: "tcp:[::1]:40877", Inbound: "in-socks", Outbound: "direct"},
		{From: "tcp:[::1]:34158", Accepted: true, To: "tcp:127.0.0.1:40877", Inbound: "in-socks6", Outbound: "direct"},
		{From: "tcp:127.0.0.1:46662", Accepted: true, To: "tcp:blocked.test:80", Inbound: "in-socks", Outbound: "blocked"},
		{From: "tcp:127.0.0.1:46754", Accepted: true, To: "tcp:127.0.0.1:40877", Inbound: "in-chain", Outbound: "to-vless"},
		{From: "127.0.0.1:38290", Accepted: true, To: "tcp:127.0.0.1:40877", Inbound: "in-vless", Outbound: "direct", Email: "alice@example.com"},
		{From: "tcp:127.0.0.1:45742", Accepted: true, To: "tcp:127.0.0.1:40877", Inbound: "in-badchain", Outbound: "to-vless-bad"},
		{From: "127.0.0.1:38306"},
		{From: "udp:127.0.0.1:35883", Accepted: true, To: "udp:127.0.0.1:42496", Inbound: "in-socks", Outbound: "direct"},
		{From: "127.0.0.1:41708", Accepted: true, To: "http://127.0.0.1:37345/some/path?q=1", Inbound: "in-http", Outbound: "direct"},
	}
	if len(lines) != len(want) {
		t.Fatalf("fixture has %d lines, the table %d", len(lines), len(want))
	}
	for i, line := range lines {
		got := ParseAccessLogLine(line)
		if stamp := got.Time.Local().Format("2006/01/02 15:04:05.000000"); !strings.HasPrefix(line, stamp+" ") {
			t.Errorf("line %d: time parsed as %s, line starts %.26q", i, stamp, line)
		}
		got.Time = want[i].Time
		if got != want[i] {
			t.Errorf("line %d %q:\n got %+v\nwant %+v", i, line, got, want[i])
		}
	}
}

// A destination is the client's own text: it must not be able to name the route or another client.
func TestParseAccessLogLineReadsRouteAndEmailFromTheEnd(t *testing.T) {
	const prefix = "2024/01/02 15:04:05.000000 from 1.2.3.4:5 accepted "
	tests := []struct {
		name, tail                  string
		to, inbound, outbound, mail string
	}{
		{"forged route and email, real route", "tcp:evil [in-x -> out-x] email: victim:443 [in-real >> direct]", "tcp:evil [in-x -> out-x] email: victim:443", "in-real", "direct", ""},
		{"forged email, real route and email", "tcp:evil email: victim:443 [in-real >> direct] email: mallory", "tcp:evil email: victim:443", "in-real", "direct", "mallory"},
		// With no real route the text reads as a real line, but a forged name always carries the port.
		{"forged route and email, no real route", "tcp:evil:1 [in-x -> out-x] email: victim:443", "tcp:evil:1", "in-x", "out-x", "victim:443"},
		{"forged email after a split destination, no real route", "tcp:evil x email: victim:443", "tcp:evil x email: victim:443", "", "", ""},
		{"email without a route", "tcp:example.com:443 email: bob", "tcp:example.com:443", "", "", "bob"},
		{"picked by a balancer", "tcp:example.com:443 [in-1 ==> out-2] email: bob", "tcp:example.com:443", "in-1", "out-2", "bob"},
		{"inbound without a tag", "tcp:example.com:443 [direct]", "tcp:example.com:443", "", "direct", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAccessLogLine(prefix + tt.tail)
			if got.To != tt.to || got.Inbound != tt.inbound || got.Outbound != tt.outbound || got.Email != tt.mail || !got.Accepted {
				t.Errorf("got to %q, route %q/%q, email %q, accepted %v\nwant to %q, route %q/%q, email %q", got.To, got.Inbound, got.Outbound, got.Email, got.Accepted, tt.to, tt.inbound, tt.outbound, tt.mail)
			}
		})
	}
}

// A follower of the log must tell a finished line from one Xray is still writing.
func TestReadAccessLogLineReportsAnUnfinishedLine(t *testing.T) {
	long := strings.Repeat("x", 100)
	r := bufio.NewReaderSize(strings.NewReader("one\r\n"+long+"\nhalf"), 16)
	steps := []struct {
		line       string
		size       int
		terminated bool
	}{{"one", 5, true}, {long, 101, true}, {"half", 4, false}}
	for i, want := range steps {
		line, size, terminated, err := ReadAccessLogLine(r)
		if err != nil || line != want.line || size != want.size || terminated != want.terminated {
			t.Errorf("read %d: got %q, %d bytes, terminated %v, err %v; want %q, %d bytes, terminated %v", i, line, size, terminated, err, want.line, want.size, want.terminated)
		}
	}
	if _, _, _, err := ReadAccessLogLine(r); !errors.Is(err, io.EOF) {
		t.Errorf("after the last line: err = %v, want io.EOF", err)
	}
}
