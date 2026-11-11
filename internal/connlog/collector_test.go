package connlog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	t   *testing.T
	cfg Config
	c   *Collector
	seq int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, c: &Collector{}, cfg: Config{
		Dir:           filepath.Join(dir, "connlog"),
		LogPath:       filepath.Join(dir, "access.log"),
		Open:          os.Open,
		Policy:        Policy{DestinationFull, SourceFull},
		RetentionDays: 30,
		MaxBytes:      1 << 30,
	}}
	t.Cleanup(func() { f.c.w.close(); f.c.closeLog() })
	return f
}

// line is an access-log line of now whose destination host names it.
func (f *fixture) line(name string) string {
	f.seq++
	return fmt.Sprintf("%s from 198.51.100.%d:%d accepted tcp:%s.test:443 [in-1 >> direct] email: alice\n",
		time.Now().Format("2006/01/02 15:04:05.000000"), f.seq%250+1, 40000+f.seq%20000, name)
}

func (f *fixture) write(text string) {
	f.t.Helper()
	file, err := os.OpenFile(f.cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		f.t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) step() {
	f.t.Helper()
	if err := f.c.Step(context.Background(), f.cfg, time.Now()); err != nil {
		f.t.Fatalf("step: %v", err)
	}
}

// restart is a new panel process over the same files.
func (f *fixture) restart() {
	f.c.w.close()
	f.c.closeLog()
	f.c = &Collector{}
}

// hosts returns the stored destinations in the order they were logged.
func (f *fixture) hosts() string {
	f.t.Helper()
	var out []string
	err := Scan(context.Background(), f.cfg.Dir, ConnFilter{}, 1<<20, func(rec ConnRecord, _ string) error {
		out = append(out, strings.TrimSuffix(rec.Host, ".test"))
		return nil
	})
	if err != nil {
		f.t.Fatalf("scan: %v", err)
	}
	slices.Reverse(out)
	return strings.Join(out, " ")
}

func (f *fixture) expect(want string) {
	f.t.Helper()
	if got := f.hosts(); got != want {
		f.t.Errorf("stored %q, want %q", got, want)
	}
}

func TestCollectorStartsAtTheEndOfAnExistingLog(t *testing.T) {
	f := newFixture(t)
	f.write(f.line("before-1") + f.line("before-2"))
	f.step()
	f.expect("")
	f.write(f.line("a") + f.line("b"))
	f.step()
	f.step()
	f.expect("a b")
}

func TestCollectorWaitsForTheEndOfALineXrayIsStillWriting(t *testing.T) {
	f := newFixture(t)
	f.write("")
	f.step()
	whole := f.line("split")
	f.write(f.line("a") + whole[:len(whole)/2])
	f.step()
	f.expect("a")
	f.write(whole[len(whole)/2:] + f.line("b"))
	f.step()
	f.expect("a split b")
}

// logrotate's default: the log is renamed and Xray, holding it open, keeps writing to it
// until it reopens the path.
func TestCollectorLosesNoLineWhenTheLogIsRenamedMidWrite(t *testing.T) {
	f := newFixture(t)
	f.write("")
	f.step()
	held, err := os.OpenFile(f.cfg.LogPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	f.write(f.line("a"))
	f.step()
	f.write(f.line("b"))
	if err := os.Rename(f.cfg.LogPath, f.cfg.LogPath+".1"); err != nil {
		t.Fatal(err)
	}
	if _, err := held.WriteString(f.line("c")); err != nil {
		t.Fatal(err)
	}
	// no file at the path yet: Xray has not reopened it
	f.step()
	f.expect("a b c")
	if _, err := held.WriteString(f.line("d")); err != nil {
		t.Fatal(err)
	}
	f.write(f.line("e") + f.line("f"))
	f.step()
	f.write(f.line("g"))
	f.step()
	f.expect("a b c d e f g")
}

// The panel's own rotation: PruneXrayLogsJob empties the log in place once it is too large.
func TestCollectorLosesNoLineWhenThePanelTruncatesTheLog(t *testing.T) {
	f := newFixture(t)
	f.write("")
	f.step()
	f.write(f.line("a"))
	f.step()
	f.write(f.line("b") + f.line("c"))
	err := f.c.TruncateLog(context.Background(), f.cfg.LogPath, f.cfg.Policy, func() error { return os.Truncate(f.cfg.LogPath, 0) })
	if err != nil {
		t.Fatal(err)
	}
	f.expect("a b c")
	f.write(f.line("d"))
	f.step()
	f.expect("a b c d")
}

func TestCollectorReadsALogCutBySomeoneElseFromItsStart(t *testing.T) {
	f := newFixture(t)
	f.write("")
	f.step()
	f.write(f.line("a") + f.line("b"))
	f.step()
	if err := os.Truncate(f.cfg.LogPath, 0); err != nil {
		t.Fatal(err)
	}
	// longer than what was read before the cut: only the first bytes give the cut away
	f.write(f.line("c") + f.line("d") + f.line("e"))
	f.step()
	f.expect("a b c d e")
}

func TestCollectorContinuesWhereItStoppedAfterARestart(t *testing.T) {
	f := newFixture(t)
	f.write("")
	f.step()
	f.write(f.line("a") + f.line("b"))
	f.step()
	f.restart()
	f.write(f.line("while-down"))
	f.step()
	f.expect("a b while-down")

	f.restart()
	if err := os.Remove(f.cfg.LogPath); err != nil {
		t.Fatal(err)
	}
	f.write(f.line("new-log-1") + f.line("new-log-2"))
	f.step()
	f.expect("a b while-down new-log-1 new-log-2")
}

// A panel that restarts before the first client connects must still know where it was.
func TestCollectorKeepsItsPlaceWithoutAnyConnection(t *testing.T) {
	f := newFixture(t)
	f.write(f.line("before"))
	f.step()
	f.restart()
	f.write(f.line("while-down"))
	f.step()
	f.expect("while-down")
}

func TestCollectorDoesNotBringInWhatWasLoggedWhileItWasOff(t *testing.T) {
	f := newFixture(t)
	f.write("")
	f.step()
	f.write(f.line("a"))
	f.step()
	if err := f.c.Halt(context.Background(), f.cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.write(f.line("while-off"))
	f.restart()
	if err := f.c.Halt(context.Background(), f.cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.step()
	f.write(f.line("b"))
	f.step()
	f.expect("a b")
}

func TestCollectorWritesNothingWhileOff(t *testing.T) {
	f := newFixture(t)
	f.write(f.line("a"))
	if err := f.c.Halt(context.Background(), f.cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.c.TruncateLog(context.Background(), f.cfg.LogPath, f.cfg.Policy, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.cfg.Dir); !os.IsNotExist(err) {
		t.Errorf("the log folder exists though the log was never on (stat error: %v)", err)
	}
}

func TestCollectorSkipsRefusedAndApiLines(t *testing.T) {
	f := newFixture(t)
	f.write("")
	f.step()
	stamp := time.Now().Format("2006/01/02 15:04:05.000000")
	f.write(stamp + " from 127.0.0.1:38306 rejected  proxy/vless/encoding: invalid request user id: b28f478d\n" +
		stamp + " from 127.0.0.1:5 accepted tcp:127.0.0.1:62789 [api -> api]\n" +
		f.line("kept"))
	f.step()
	f.expect("kept")
}

// A request can write a line of its own into Xray's access log (TestAccessLog_E2E). Dated
// ahead of the clock it would be the newest day for years: every later row filed with it.
func TestCollectorLeavesOutALineDatedAheadOfTheClock(t *testing.T) {
	f := newFixture(t)
	f.write("")
	f.step()
	forged := time.Now().AddDate(1, 0, 0).Format("2006/01/02 15:04:05.000000") +
		" from 203.0.113.9:1 accepted tcp:forged.test:443 [in-y >> out-y] email: victim\n"
	f.write(f.line("a") + forged + f.line("b"))
	if err := f.c.Step(context.Background(), f.cfg, time.Now()); !errors.Is(err, ErrLineAheadOfClock) {
		t.Errorf("step error %v, want ErrLineAheadOfClock", err)
	}
	f.expect("a b")
	if got, today := days(f.cfg.Dir), time.Now().UTC().Format(dayLayout); len(got) != 1 || got[0] > today {
		t.Errorf("day files %v, want one no later than %s", got, today)
	}
	f.write(f.line("c"))
	f.step()
	f.expect("a b c")
}

// What the retention would delete at once is not filed first: an old date must not make a
// day file of its own, which the next prune would remove with the position in it.
func TestCollectorLeavesOutLinesOlderThanTheRetention(t *testing.T) {
	f := newFixture(t)
	f.write("")
	f.step()
	old := time.Now().AddDate(0, 0, -f.cfg.RetentionDays-2).Format("2006/01/02 15:04:05.000000") +
		" from 203.0.113.9:1 accepted tcp:old.test:443 [in-1 >> direct] email: alice\n"
	f.write(f.line("a") + old)
	f.step()
	f.expect("a")
	f.write(f.line("b"))
	f.step()
	f.expect("a b")
}

// The panel was down for days while Xray kept writing. Each day's lines belong in that
// day's file: a query for a day opens that file and the one after it, no further.
func TestCollectorFilesABacklogOfSeveralDaysByDay(t *testing.T) {
	f := newFixture(t)
	f.write("")
	if err := f.c.Step(context.Background(), f.cfg, time.Now().AddDate(0, 0, -3)); err != nil {
		t.Fatal(err)
	}
	var backlog strings.Builder
	for age := 3; age >= 0; age-- {
		fmt.Fprintf(&backlog, "%s from 198.51.100.7:4000%d accepted tcp:day-%d.test:443 [in-1 >> direct] email: alice\n",
			time.Now().AddDate(0, 0, -age).Format("2006/01/02 15:04:05.000000"), age, age)
	}
	f.write(backlog.String())
	f.step()
	f.expect("day-3 day-2 day-1 day-0")
	if got := days(f.cfg.Dir); len(got) != 4 {
		t.Errorf("day files %v, want one for each day of the backlog", got)
	}
	then := time.Now().AddDate(0, 0, -3)
	var found []string
	err := Scan(context.Background(), f.cfg.Dir, ConnFilter{From: then.Add(-time.Hour).UnixMilli(), To: then.Add(time.Hour).UnixMilli()}, 10,
		func(rec ConnRecord, _ string) error {
			found = append(found, rec.Host)
			return nil
		})
	if err != nil || strings.Join(found, " ") != "day-3.test" {
		t.Errorf("a query for the first day of the backlog found %q (err %v), want day-3.test", found, err)
	}
}

// A day file created and never written (a full disk, a kill) holds no position; the file
// before it does, and a restart must continue from there instead of from the end of the log.
func TestCollectorFindsItsPlaceBehindADayFileNeverWritten(t *testing.T) {
	f := newFixture(t)
	f.write("")
	f.step()
	f.write(f.line("a"))
	f.step()
	f.restart()
	w, err := openWriter(context.Background(), f.cfg.Dir, time.Now().UTC().AddDate(0, 0, 1).Format(dayLayout), true)
	if err != nil {
		t.Fatal(err)
	}
	w.close()
	f.write(f.line("while-down"))
	f.step()
	f.expect("a while-down")
}

// The panel's truncation names the file it cuts. After the access-log path changed, that
// is not yet the file being followed, whose position must stay.
func TestCollectorKeepsItsPlaceWhenAnotherLogIsTruncated(t *testing.T) {
	f := newFixture(t)
	f.write("")
	f.step()
	f.write(f.line("a"))
	f.step()
	if err := f.c.TruncateLog(context.Background(), f.cfg.LogPath+".new", f.cfg.Policy, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	f.write(f.line("b"))
	f.step()
	f.expect("a b")
}
