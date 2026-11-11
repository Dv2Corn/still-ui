package xray

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"time"
)

// Xray's HTTP inbound logs the requested URL whole, so an access-log line has
// no length bound; a reader keeps this much of a line's start and of its end.
const (
	AccessLogLineBytes    = 64 << 10
	AccessLogLineEndBytes = 1 << 10
)

// AccessLogLine is one line of the Xray access log. To, Inbound, Outbound and Email
// are set only on an accepted connection.
type AccessLogLine struct {
	Time     time.Time
	From     string
	Accepted bool
	To       string
	Inbound  string
	Outbound string
	Email    string
}

// routeSeparators are how Xray joins the inbound and outbound tag: picked by a
// balancer, matched by a routing rule, or sent to the default outbound.
var routeSeparators = []string{" ==> ", " -> ", " >> "}

// ParseAccessLogLine reads the route and the email from the END of the line: the
// destination before them is the client's own text and may imitate either.
func ParseAccessLogLine(line string) AccessLogLine {
	var entry AccessLogLine
	rest := line
	if date, afterDate, ok := strings.Cut(line, " "); ok {
		if clock, afterClock, ok := strings.Cut(afterDate, " "); ok {
			if t, err := time.ParseInLocation("2006/01/02 15:04:05.999999", date+" "+clock, time.Local); err == nil {
				entry.Time = t.UTC()
				rest = afterClock
			}
		}
	}
	rest, ok := strings.CutPrefix(rest, "from ")
	if !ok {
		return entry
	}
	from, rest, _ := strings.Cut(rest, " ")
	entry.From = strings.TrimLeft(from, "/")
	rest, ok = strings.CutPrefix(rest, "accepted ")
	if !ok || rest == "" {
		return entry
	}
	entry.Accepted = true

	const emailMark = " email: "
	if i := strings.LastIndex(rest, emailMark); i >= 0 {
		email, before := rest[i+len(emailMark):], rest[:i]
		// Xray writes the email last, after the route; with no route the destination must be one word.
		if email != "" && !strings.Contains(email, " ") && (strings.HasSuffix(before, "]") || !strings.Contains(before, " ")) {
			entry.Email, rest = email, before
		}
	}
	if strings.HasSuffix(rest, "]") {
		if i := strings.LastIndex(rest, " ["); i >= 0 {
			route := rest[i+2 : len(rest)-1]
			entry.Outbound = route
			for _, sep := range routeSeparators {
				if in, out, ok := strings.Cut(route, sep); ok {
					entry.Inbound, entry.Outbound = in, out
					break
				}
			}
			rest = rest[:i]
		}
	}
	entry.To = rest
	return entry
}

// ReadAccessLogLine returns the next line of r without its line ending and the bytes it took.
// A line longer than r's buffer loses its middle; terminated is false when the input ends mid-line.
func ReadAccessLogLine(r *bufio.Reader) (line string, size int, terminated bool, err error) {
	chunk, err := r.ReadSlice('\n')
	size = len(chunk)
	head := string(chunk)
	var end []byte
	for errors.Is(err, bufio.ErrBufferFull) {
		chunk, err = r.ReadSlice('\n')
		size += len(chunk)
		end = append(end, chunk...)
		// two spare bytes: the line ending is cut off below, after the window is taken
		end = end[max(0, len(end)-AccessLogLineEndBytes-2):]
	}
	if err != nil && (!errors.Is(err, io.EOF) || size == 0) {
		return "", size, false, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(head+string(end), "\n"), "\r")
	if over := len(line) - len(head) - AccessLogLineEndBytes; over > 0 {
		line = head + line[len(head)+over:]
	}
	return line, size, err == nil, nil
}
