package connlog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

var testDay = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

// seedDay writes n records into the file of the given day, one per second from noon,
// and returns them.
func seedDay(t *testing.T, dir string, day time.Time, n int, shape func(i int, rec *ConnRecord)) []ConnRecord {
	t.Helper()
	recs := make([]ConnRecord, n)
	for i := range recs {
		recs[i] = ConnRecord{
			Time: day.Add(time.Duration(i) * time.Second).UnixMilli(), Client: "alice", Inbound: "in-1", Outbound: "direct",
			Source: "198.51.100.7", SourcePort: 40000 + i%1000, Network: "tcp", Host: fmt.Sprintf("host-%d.test", i), Port: 443,
		}
		if shape != nil {
			shape(i, &recs[i])
		}
	}
	w, err := openWriter(context.Background(), dir, day.Format(dayLayout), true)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	if err := w.append(context.Background(), recs, tailState{}); err != nil {
		t.Fatal(err)
	}
	return recs
}

func queryHosts(t *testing.T, dir string, f ConnFilter) []string {
	t.Helper()
	var hosts []string
	if err := Scan(context.Background(), dir, f, 1<<20, func(rec ConnRecord, _ string) error {
		hosts = append(hosts, rec.Host)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return hosts
}

func TestQueryPagesThroughDaysNewestFirstWithoutGapOrRepeat(t *testing.T) {
	dir := t.TempDir()
	var all []ConnRecord
	for d := range 3 {
		all = append(all, seedDay(t, dir, testDay.AddDate(0, 0, d), 7, func(i int, rec *ConnRecord) { rec.Host = fmt.Sprintf("d%d-%d.test", d, i) })...)
	}
	var got []ConnRecord
	cursor := ""
	for pages := 0; ; pages++ {
		page, err := Query(context.Background(), dir, ConnFilter{Limit: 4, Before: cursor})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page.Rows...)
		if page.Next == "" {
			if pages != 5 {
				t.Errorf("21 rows by 4 took %d pages, want 6", pages+1)
			}
			break
		}
		if len(page.Rows) != 4 {
			t.Fatalf("page %d has %d rows though another follows", pages, len(page.Rows))
		}
		cursor = page.Next
	}
	if len(got) != len(all) {
		t.Fatalf("paged through %d rows, stored %d", len(got), len(all))
	}
	for i, rec := range got {
		if want := all[len(all)-1-i]; rec != want {
			t.Fatalf("row %d is %+v, want %+v", i, rec, want)
		}
	}
}

func TestQueryFilters(t *testing.T) {
	dir := t.TempDir()
	seedDay(t, dir, testDay, 6, func(i int, rec *ConnRecord) {
		rec.Client = []string{"alice", "bob"}[i%2]
		rec.Inbound = []string{"in-1", "in-2", "in-3"}[i%3]
		rec.Host = []string{"cdn.Example.com", "100%.test", "a_b.test", "axb.test", "other.org", "example.net"}[i]
		rec.Source = []string{"198.51.100.7", "2001:db8::7"}[i%2]
	})
	at := func(i int) int64 { return testDay.Add(time.Duration(i) * time.Second).UnixMilli() }
	tests := []struct {
		name   string
		filter ConnFilter
		want   string
	}{
		{"client is exact", ConnFilter{Client: "bob"}, "example.net axb.test 100%.test"},
		{"part of a client name does not match", ConnFilter{Client: "bo"}, ""},
		{"inbound", ConnFilter{Inbound: "in-3"}, "example.net a_b.test"},
		{"host contains, any case", ConnFilter{Host: "EXAMPLE"}, "example.net cdn.Example.com"},
		{"percent is a character, not a wildcard", ConnFilter{Host: "%"}, "100%.test"},
		{"underscore is a character, not a wildcard", ConnFilter{Host: "a_b"}, "a_b.test"},
		{"source contains", ConnFilter{Source: "2001:db8"}, "example.net axb.test 100%.test"},
		{"time range is inclusive", ConnFilter{From: at(1), To: at(3)}, "axb.test a_b.test 100%.test"},
		{"filters combine", ConnFilter{Client: "alice", Host: ".test", From: at(1)}, "a_b.test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Join(queryHosts(t, dir, tt.filter), " "); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// A batch is filed under the day of its last line: rows of 23:59 can open the next day's file.
func TestQueryTimeRangeFindsRowsFiledInTheNextDay(t *testing.T) {
	dir := t.TempDir()
	lateEvening := testDay.Add(11*time.Hour + 59*time.Minute)
	seedDay(t, dir, testDay.AddDate(0, 0, 1), 1, func(_ int, rec *ConnRecord) {
		rec.Time, rec.Host = lateEvening.UnixMilli(), "late.test"
	})
	seedDay(t, dir, testDay.AddDate(0, 0, 3), 1, nil)
	dayStart := testDay.Add(-12 * time.Hour)
	got := queryHosts(t, dir, ConnFilter{From: dayStart.UnixMilli(), To: dayStart.Add(24*time.Hour - time.Millisecond).UnixMilli()})
	if strings.Join(got, " ") != "late.test" {
		t.Errorf("got %q, want the row of that evening", got)
	}
}

func TestQueryRejectsACursorItDidNotIssue(t *testing.T) {
	for _, cursor := range []string{"x", "20260310", "20260310:0", "20260310:-4", "2026031:5", "20260310:5:6"} {
		if _, err := Query(context.Background(), t.TempDir(), ConnFilter{Before: cursor}); !errors.Is(err, ErrBadCursor) {
			t.Errorf("cursor %q: err = %v, want ErrBadCursor", cursor, err)
		}
	}
}

func TestPruneDeletesDaysPastTheRetention(t *testing.T) {
	dir := t.TempDir()
	now := testDay.AddDate(0, 0, 40)
	for _, age := range []int{31, 30, 29, 0} {
		seedDay(t, dir, now.AddDate(0, 0, -age), 3, nil)
	}
	c := &Collector{}
	if err := c.prune(context.Background(), Config{Dir: dir, RetentionDays: 30, MaxBytes: 1 << 30}, now); err != nil {
		t.Fatal(err)
	}
	want := []string{now.AddDate(0, 0, -30).Format(dayLayout), now.AddDate(0, 0, -29).Format(dayLayout), now.Format(dayLayout)}
	if got := days(dir); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("days left %v, want %v", got, want)
	}
}

func TestPruneDeletesTheOldestDaysOverTheSizeCap(t *testing.T) {
	dir := t.TempDir()
	for d := range 4 {
		seedDay(t, dir, testDay.AddDate(0, 0, d), 3000, nil)
	}
	perDay := fileBytes(dir, testDay.Format(dayLayout))
	limit := perDay*2 + perDay/2
	c := &Collector{}
	if err := c.prune(context.Background(), Config{Dir: dir, RetentionDays: 3650, MaxBytes: limit}, testDay.AddDate(0, 0, 3)); err != nil {
		t.Fatal(err)
	}
	want := []string{testDay.AddDate(0, 0, 2).Format(dayLayout), testDay.AddDate(0, 0, 3).Format(dayLayout)}
	if got := days(dir); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("days left %v, want the two newest %v", got, want)
	}
	if used := Usage(dir); used > limit {
		t.Errorf("log takes %d bytes, cap is %d", used, limit)
	}
}

// One day larger than the whole cap: the file being written must stop growing and keep its newest rows.
func TestSizeCapHoldsWithinASingleDay(t *testing.T) {
	f := newFixture(t)
	f.cfg.MaxBytes = 4 << 20
	f.write("")
	f.step()
	var last string
	for round := range 120 {
		var lines strings.Builder
		for i := range 1000 {
			last = fmt.Sprintf("r%d-%d", round, i)
			lines.WriteString(f.line(last))
		}
		f.write(lines.String())
		f.step()
		if err := f.c.prune(context.Background(), f.cfg, time.Now()); err != nil {
			t.Fatal(err)
		}
		if used := Usage(f.cfg.Dir); used > f.cfg.MaxBytes {
			t.Fatalf("after %d rows the log takes %d bytes, cap is %d", (round+1)*1000, used, f.cfg.MaxBytes)
		}
	}
	page, err := Query(context.Background(), f.cfg.Dir, ConnFilter{Limit: 1})
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Host != last+".test" {
		t.Fatalf("newest row is %+v (err %v), want %s.test", page.Rows, err, last)
	}
	if oldest := queryHosts(t, f.cfg.Dir, ConnFilter{Host: "r0-0.test"}); len(oldest) != 0 {
		t.Errorf("the first row survived 120000 rows under a 4 MiB cap")
	}
	if _, err := os.Stat(dayPath(f.cfg.Dir, time.Now().UTC().Format(dayLayout))); err != nil {
		t.Errorf("the day being written was deleted instead of trimmed: %v", err)
	}
}

// Timing and size at the volume the feature is sized for; XUI_SCALE_TEST=1 runs it.
func TestScaleOneMillionConnections(t *testing.T) {
	if os.Getenv("XUI_SCALE_TEST") == "" {
		t.Skip("set XUI_SCALE_TEST=1 to run the 1M-row write and query measurement")
	}
	const rows, batch = 1_000_000, batchRows
	dir := t.TempDir()
	ctx := context.Background()
	w, err := openWriter(ctx, dir, testDay.Format(dayLayout), true)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	recs := make([]ConnRecord, 0, batch)
	started := time.Now()
	for i := range rows {
		recs = append(recs, ConnRecord{
			// 2000 clients, 20000 hosts with a long tail, 5000 source addresses
			Time: testDay.Add(time.Duration(i) * 86 * time.Millisecond).UnixMilli(), Client: fmt.Sprintf("client-%d@example.com", (i*7919)%2000),
			Inbound: fmt.Sprintf("inbound-%d", 443+i%4), Outbound: "direct", Source: fmt.Sprintf("203.0.%d.%d", (i*31)%20, (i*17)%250),
			SourcePort: 1024 + i%60000, Network: "tcp", Host: fmt.Sprintf("s%d.site-%d.example.com", i%3, (i*i)%20000), Port: 443,
		})
		if len(recs) == batch {
			if err := w.append(ctx, recs, tailState{Offset: int64(i)}); err != nil {
				t.Fatal(err)
			}
			recs = recs[:0]
		}
	}
	wrote := time.Since(started)
	t.Logf("write: %d rows in %v (%.1f µs/row), %d bytes on disk (%.1f bytes/row)", rows, wrote, float64(wrote.Microseconds())/rows, Usage(dir), float64(Usage(dir))/rows)

	const bound = 2 * time.Second
	for name, filter := range map[string]ConnFilter{
		"no filter":              {},
		"client + time range":    {Client: "client-77@example.com", From: testDay.Add(6 * time.Hour).UnixMilli(), To: testDay.Add(12 * time.Hour).UnixMilli()},
		"host + time range":      {Host: "site-10000.", From: testDay.Add(6 * time.Hour).UnixMilli(), To: testDay.Add(12 * time.Hour).UnixMilli()},
		"host matching a lot":    {Host: "example.com"},
		"source address":         {Source: "203.0.7.119"},
		"source nothing matches": {Source: "198.18."},
		"nothing matches":        {Client: "nobody"},
		"host nothing matches":   {Host: "no-such-host"},
	} {
		started := time.Now()
		page, err := Query(ctx, dir, filter)
		took := time.Since(started)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("query %-22s first page of %d rows in %v", name, len(page.Rows), took)
		if took > bound {
			t.Errorf("%s: first page took %v, bound is %v", name, took, bound)
		}
	}
}
