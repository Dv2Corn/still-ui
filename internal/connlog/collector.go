package connlog

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const (
	// batchRows bounds what one transaction, and the memory of one step, holds.
	batchRows = 2000
	// headBytes of a log's first line (its timestamp and source) tell it from any other log.
	headBytes = 64

	pruneEvery = time.Minute
	// clockSlack is how far ahead of the panel's clock a line's time may be.
	clockSlack = time.Minute
	dayMillis  = 24 * 60 * 60 * 1000
)

// ErrLineAheadOfClock reports a line that was left out for its time. Xray stamps a line as
// it writes it, so a time yet to come was put there by a request, not by Xray.
var ErrLineAheadOfClock = errors.New("a line dated ahead of the clock was not stored: Xray writes a refused request's text into the access log as it came, line breaks included")

// Config is what one step of the collector works with; the caller reads it from the settings.
type Config struct {
	// Dir holds the day files; LogPath is Xray's access log, opened through Open.
	Dir     string
	LogPath string
	Open    func(path string) (*os.File, error)

	Policy        Policy
	RetentionDays int
	MaxBytes      int64
}

// Collector follows the access log and writes the day files. The zero value is ready.
type Collector struct {
	mu  sync.Mutex
	cfg Config
	w   *writer

	log     *os.File
	reader  *bufio.Reader
	off     int64
	head    []byte
	saved   tailState
	resumed bool
	halted  bool

	// now is the clock of the step being run; ahead says it met a line dated after it.
	now    time.Time
	ahead  bool
	pruned time.Time
	batch  []ConnRecord
}

// Step reads what the access log gained since the last step and, once a minute, prunes.
// A step that finds another one running does nothing.
func (c *Collector) Step(ctx context.Context, cfg Config, now time.Time) error {
	if !c.mu.TryLock() {
		return nil
	}
	defer c.mu.Unlock()
	c.cfg, c.halted, c.now = cfg, false, now
	err := c.collect(ctx)
	if c.ahead {
		c.ahead = false
		err = errors.Join(err, ErrLineAheadOfClock)
	}
	return errors.Join(err, c.pruneDue(ctx, cfg, now))
}

// Halt stops collecting. The log position is replaced by "start at the end", so turning
// the log on again later does not bring in what Xray wrote while it was off.
func (c *Collector) Halt(ctx context.Context, cfg Config, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var err error
	if !c.halted {
		c.halted = true
		c.closeLog()
		c.resumed = false
		c.w.close()
		c.w = nil
		w, state, openErr := openPositioned(ctx, cfg.Dir)
		if openErr == nil {
			if state.Offset >= 0 {
				err = w.append(ctx, nil, tailState{Offset: -1})
			}
			w.close()
		} else if !errors.Is(openErr, errNoLog) {
			err = openErr
		}
	}
	return errors.Join(err, c.pruneDue(ctx, cfg, now))
}

// TruncateLog runs truncate, which empties the access log at path, right after reading the
// log to its end: a line is lost only if Xray writes it between that last read and the cut.
func (c *Collector) TruncateLog(ctx context.Context, path string, policy Policy, truncate func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.log == nil || path != c.cfg.LogPath {
		return truncate()
	}
	c.cfg.Policy, c.now = policy, time.Now()
	// A log renamed away since the last step is not the file being cut: its position stays.
	replaced, _ := c.logChanged()
	readErr := c.readAll(ctx)
	if err := truncate(); err != nil {
		return errors.Join(readErr, err)
	}
	if !replaced {
		c.startOfLog()
	}
	return readErr
}

// startOfLog points at the first byte of a log nothing has been read from yet.
func (c *Collector) startOfLog() {
	c.off, c.head, c.saved = 0, nil, tailState{}
}

func (c *Collector) closeLog() {
	if c.log != nil {
		_ = c.log.Close()
		c.log = nil
	}
}

func (c *Collector) collect(ctx context.Context) error {
	if c.cfg.LogPath == "" {
		return nil
	}
	if c.log != nil {
		replaced, truncated := c.logChanged()
		if replaced {
			// Xray still held the old file when the path got a new one: finish the old one first.
			if err := c.readAll(ctx); err != nil {
				return err
			}
			c.closeLog()
		} else if truncated {
			c.startOfLog()
		}
	}
	if c.log == nil {
		f, err := c.cfg.Open(c.cfg.LogPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		c.log = f
		c.startOfLog()
		if !c.resumed {
			if err := c.resume(ctx); err != nil {
				c.closeLog()
				return err
			}
			c.resumed = true
		}
	}
	return c.readAll(ctx)
}

// resume picks the place to continue from after a start: the saved position when the log
// is still the one it was saved for, its start when it is a new one, its end the first time.
func (c *Collector) resume(ctx context.Context) error {
	info, err := c.log.Stat()
	if err != nil {
		return err
	}
	c.w.close()
	c.w = nil
	w, state, err := openPositioned(ctx, c.cfg.Dir)
	if err != nil && !errors.Is(err, errNoLog) {
		return err
	}
	c.w = w
	if err == nil && state.Offset >= 0 {
		if info.Size() >= state.Offset && c.headIs(state.Head) {
			c.off, c.head, c.saved = state.Offset, state.Head, state
		}
		return nil
	}
	c.off = info.Size()
	c.saved.Offset = c.off
	// Saved at once, so the position survives a restart even before the first connection.
	return c.commit(ctx, nil, true)
}

func (c *Collector) headIs(want []byte) bool {
	got := make([]byte, len(want))
	if _, err := c.log.ReadAt(got, 0); err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	return bytes.Equal(got, want)
}

// logChanged reports a log that was replaced at its path (rotated by rename) and one that
// was cut in place: shorter than what was read, or starting with other bytes.
func (c *Collector) logChanged() (replaced, truncated bool) {
	info, err := c.log.Stat()
	if err != nil {
		return true, false
	}
	if atPath, err := os.Stat(c.cfg.LogPath); err == nil && !os.SameFile(info, atPath) {
		return true, false
	}
	return false, info.Size() < c.off || !c.headIs(c.head)
}

// readAll consumes every complete line from the current position. An unfinished last line
// stays unread until Xray ends it.
func (c *Collector) readAll(ctx context.Context) error {
	section := io.NewSectionReader(c.log, c.off, math.MaxInt64-c.off)
	if c.reader == nil {
		c.reader = bufio.NewReaderSize(section, xray.AccessLogLineBytes)
	} else {
		c.reader.Reset(section)
	}
	batch := c.batch[:0]
	defer func() { c.batch = batch[:0] }()
	flush := func() error {
		err := c.commit(ctx, batch, false)
		if err != nil {
			// back to the last stored position: these lines are read again next time
			c.off = c.saved.Offset
		}
		batch = batch[:0]
		return err
	}
	// A day the retention deletes at once is not written at all: it would only be filed and removed.
	oldest := c.now.UTC().AddDate(0, 0, -c.cfg.RetentionDays).Truncate(24 * time.Hour).UnixMilli()
	latest := c.now.Add(clockSlack).UnixMilli()
	for {
		line, size, terminated, err := xray.ReadAccessLogLine(c.reader)
		more := err == nil && terminated
		if more {
			rec, ok := recordFrom(xray.ParseAccessLogLine(line), c.cfg.Policy)
			if ok && rec.Time > latest {
				c.ahead, ok = true, false
			}
			ok = ok && rec.Time >= oldest
			// One batch is one day, so a backlog of several days lands in each day's own file.
			if ok && len(batch) > 0 && rec.Time/dayMillis != batch[0].Time/dayMillis {
				if err := flush(); err != nil {
					return err
				}
			}
			c.off += int64(size)
			if ok {
				batch = append(batch, rec)
			}
		}
		if !more || len(batch) == batchRows {
			if err := flush(); err != nil {
				return err
			}
		}
		if !more {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// commit stores a batch with the position after it. The file is the day of the batch's
// lines, never an earlier day than the one written last, which keeps the position in it.
func (c *Collector) commit(ctx context.Context, batch []ConnRecord, force bool) error {
	// Lines that left no record need no write: after a restart they are read again and skipped again.
	if len(batch) == 0 && !force {
		return nil
	}
	if len(c.head) < headBytes && c.off > int64(len(c.head)) {
		head := make([]byte, min(headBytes, c.off))
		if _, err := c.log.ReadAt(head, 0); err == nil {
			c.head = head
		}
	}
	state := tailState{Offset: c.off, Head: c.head}
	day := c.now.UTC().Format(dayLayout)
	if len(batch) > 0 {
		day = dayOf(batch[len(batch)-1].Time)
	}
	if c.w == nil {
		if list := days(c.cfg.Dir); len(list) > 0 {
			day = max(day, list[len(list)-1])
		}
	} else {
		day = max(day, c.w.day)
	}
	if c.w == nil || c.w.day != day {
		w, err := openWriter(ctx, c.cfg.Dir, day, true)
		if err != nil {
			return err
		}
		c.w.close()
		c.w = w
	}
	if err := c.w.append(ctx, batch, state); err != nil {
		return err
	}
	c.saved = state
	return nil
}

func (c *Collector) pruneDue(ctx context.Context, cfg Config, now time.Time) error {
	if now.Sub(c.pruned) < pruneEvery && !now.Before(c.pruned) {
		return nil
	}
	c.pruned = now
	return c.prune(ctx, cfg, now)
}

// prune deletes the days older than the retention, then the oldest days while the log is
// over its size cap. The last file left is trimmed when it is being written, deleted otherwise.
func (c *Collector) prune(ctx context.Context, cfg Config, now time.Time) error {
	remove := func(day string) {
		if c.w != nil && c.w.day == day {
			c.w.close()
			c.w = nil
		}
		removeDay(cfg.Dir, day)
	}
	list := days(cfg.Dir)
	cutoff := now.UTC().AddDate(0, 0, -cfg.RetentionDays).Format(dayLayout)
	for len(list) > 0 && list[0] < cutoff {
		remove(list[0])
		list = list[1:]
	}
	sizes := make([]int64, len(list))
	var total int64
	for i, day := range list {
		sizes[i] = fileBytes(cfg.Dir, day)
		total += sizes[i]
	}
	for len(list) > 1 && total > cfg.MaxBytes {
		remove(list[0])
		total -= sizes[0]
		list, sizes = list[1:], sizes[1:]
	}
	// Headroom for the rows that arrive before the next prune and for the write-ahead log at its fullest.
	limit := cfg.MaxBytes - cfg.MaxBytes/10 - walBudget
	if len(list) != 1 || total <= limit {
		return nil
	}
	if c.w != nil && c.w.day == list[0] {
		return c.w.trim(ctx, limit-limit/20)
	}
	if total > cfg.MaxBytes {
		remove(list[0])
	}
	return nil
}
