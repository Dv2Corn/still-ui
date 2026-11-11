package connlog

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	// database/sql driver "sqlite3", the one the panel database already links.
	_ "github.com/mattn/go-sqlite3"
)

const (
	filePrefix = "conn-"
	fileSuffix = ".db"
	dayLayout  = "20060102"

	// walBudget is the write-ahead log's limit (set in openWriter) plus one transaction past it.
	walBudget = 1 << 20

	// maxCachedStrings bounds the writer's string-to-id map; past it the map starts over.
	maxCachedStrings = 50000
)

// Strings (client, tags, addresses, hosts) repeat on almost every row, so a row holds their
// ids. Rows are appended in log order: id order is time order within a file.
const schema = `
CREATE TABLE IF NOT EXISTS str (id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS conn (
	id INTEGER PRIMARY KEY,
	ts INTEGER NOT NULL,
	client INTEGER NOT NULL,
	inbound INTEGER NOT NULL,
	outbound INTEGER NOT NULL,
	src INTEGER NOT NULL,
	sport INTEGER NOT NULL,
	net INTEGER NOT NULL,
	dst INTEGER NOT NULL,
	dport INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS conn_client ON conn (client);
CREATE INDEX IF NOT EXISTS conn_dst ON conn (dst);
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
`

func dayOf(ms int64) string { return time.UnixMilli(ms).UTC().Format(dayLayout) }

func dayPath(dir, day string) string { return filepath.Join(dir, filePrefix+day+fileSuffix) }

// sqliteURI names a database file for the driver; a bare path would end at its first "?".
func sqliteURI(path, params string) string {
	return "file:" + strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path) + "?" + params
}

// days lists the days that have a file in dir, oldest first.
func days(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		day, ok := strings.CutPrefix(entry.Name(), filePrefix)
		if day, ok2 := strings.CutSuffix(day, fileSuffix); ok && ok2 {
			if _, err := time.Parse(dayLayout, day); err == nil {
				out = append(out, day)
			}
		}
	}
	slices.Sort(out)
	return out
}

// fileBytes is what one day takes on disk, its write-ahead log included.
func fileBytes(dir, day string) int64 {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(dayPath(dir, day) + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

func removeDay(dir, day string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(dayPath(dir, day) + suffix)
	}
}

// Usage returns the bytes the log takes on disk.
func Usage(dir string) int64 {
	var total int64
	for _, day := range days(dir) {
		total += fileBytes(dir, day)
	}
	return total
}

// tailState is how far the access log has been read. Head holds the file's first bytes,
// which tell the same log from a new one at the same path; Offset -1 means "start at the end".
type tailState struct {
	Offset int64
	Head   []byte
}

// writer appends to one day file. Not safe for concurrent use: the collector serialises it.
type writer struct {
	dir string
	day string
	db  *sql.DB
	ids map[string]int64
}

// openWriter opens the file of the given day, creating it when create is set.
func openWriter(ctx context.Context, dir, day string, create bool) (*writer, error) {
	mode := "rw"
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		mode = "rwc"
	}
	path := dayPath(dir, day)
	db, err := sql.Open("sqlite3", sqliteURI(path, "mode="+mode+"&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// A short write-ahead log, cut back after each checkpoint: it counts against the size cap.
	if _, err := db.ExecContext(ctx, schema+"PRAGMA wal_autocheckpoint = 128; PRAGMA journal_size_limit = 524288;"); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &writer{dir: dir, day: day, db: db, ids: make(map[string]int64)}, nil
}

func (w *writer) close() {
	if w != nil && w.db != nil {
		_ = w.db.Close()
	}
}

func (w *writer) id(ctx context.Context, tx *sql.Tx, v string) (int64, error) {
	if id, ok := w.ids[v]; ok {
		return id, nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO str (v) VALUES (?) ON CONFLICT (v) DO NOTHING", v); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM str WHERE v = ?", v).Scan(&id); err != nil {
		return 0, err
	}
	if len(w.ids) >= maxCachedStrings {
		clear(w.ids)
	}
	w.ids[v] = id
	return id, nil
}

// append stores the records and the read position in one transaction, so a crash can
// neither repeat a line nor skip one.
func (w *writer) append(ctx context.Context, recs []ConnRecord, state tailState) (err error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
			// ids handed out inside the rolled-back transaction do not exist
			clear(w.ids)
		}
	}()
	insert, err := tx.PrepareContext(ctx, "INSERT INTO conn (ts, client, inbound, outbound, src, sport, net, dst, dport) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer insert.Close()
	for i := range recs {
		rec := &recs[i]
		var ids [5]int64
		for j, v := range [5]string{rec.Client, rec.Inbound, rec.Outbound, rec.Source, rec.Host} {
			if ids[j], err = w.id(ctx, tx, v); err != nil {
				return err
			}
		}
		network := 0
		if rec.Network == "udp" {
			network = 1
		}
		if _, err = insert.ExecContext(ctx, rec.Time, ids[0], ids[1], ids[2], ids[3], rec.SourcePort, network, ids[4], rec.Port); err != nil {
			return err
		}
	}
	const upsert = "INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v"
	if _, err = tx.ExecContext(ctx, upsert, "offset", strconv.FormatInt(state.Offset, 10)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, upsert, "head", hex.EncodeToString(state.Head)); err != nil {
		return err
	}
	return tx.Commit()
}

func (w *writer) state(ctx context.Context) (tailState, bool) {
	var offset, head string
	if err := w.db.QueryRowContext(ctx, "SELECT v FROM meta WHERE k = 'offset'").Scan(&offset); err != nil {
		return tailState{}, false
	}
	_ = w.db.QueryRowContext(ctx, "SELECT v FROM meta WHERE k = 'head'").Scan(&head)
	n, err := strconv.ParseInt(offset, 10, 64)
	if err != nil {
		return tailState{}, false
	}
	raw, _ := hex.DecodeString(head)
	return tailState{Offset: n, Head: raw}, true
}

// trimChunkRows keeps one delete, and so the write-ahead log it fills, small.
const trimChunkRows = 2000

// trim deletes the oldest rows until the pages in use fit in target bytes. SQLite reuses
// the freed pages, so the file stops growing; it does not shrink.
func (w *writer) trim(ctx context.Context, target int64) error {
	// The share of rows to delete is estimated from the bytes over target, then measured again.
	for range 4 {
		var free, pages, pageSize int64
		if err := w.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
			return err
		}
		if err := w.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
			return err
		}
		if err := w.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
			return err
		}
		used := (pages - free) * pageSize
		if used <= target {
			return nil
		}
		var lo, hi sql.NullInt64
		if err := w.db.QueryRowContext(ctx, "SELECT min(id), max(id) FROM conn").Scan(&lo, &hi); err != nil || !lo.Valid {
			return err
		}
		last := lo.Int64 + (hi.Int64-lo.Int64+1)*(used-target)/used + trimChunkRows
		for from := lo.Int64; from <= last; from += trimChunkRows {
			if _, err := w.db.ExecContext(ctx, "DELETE FROM conn WHERE id <= ?", min(from+trimChunkRows-1, last)); err != nil {
				return err
			}
		}
		// Strings only the deleted rows used go too, or a day of one-off destinations never stops growing.
		clear(w.ids)
		if _, err := w.db.ExecContext(ctx, `DELETE FROM str WHERE id NOT IN (
			SELECT client FROM conn UNION SELECT inbound FROM conn UNION SELECT outbound FROM conn
			UNION SELECT src FROM conn UNION SELECT dst FROM conn)`); err != nil {
			return err
		}
	}
	return nil
}

var errNoLog = errors.New("connection log is empty")

// openPositioned opens the latest day file that holds a read position, with that position;
// errNoLog when none does. A newer file without one was created and never written.
func openPositioned(ctx context.Context, dir string) (*writer, tailState, error) {
	list := days(dir)
	for i := len(list) - 1; i >= 0; i-- {
		w, err := openWriter(ctx, dir, list[i], false)
		if err != nil {
			return nil, tailState{}, err
		}
		if state, ok := w.state(ctx); ok {
			return w, state, nil
		}
		w.close()
	}
	return nil, tailState{}, errNoLog
}
