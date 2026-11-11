package connlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultPageRows = 50
	MaxPageRows     = 500
)

// ConnFilter narrows a query; an empty field does not filter. Client and Inbound match
// exactly, Host and Source match any part of the stored text, ignoring ASCII case.
type ConnFilter struct {
	Client  string `json:"client" example:"alice@example.com"`
	Inbound string `json:"inbound" example:"inbound-443"`
	Host    string `json:"host" example:"example.com"`
	Source  string `json:"source" example:"203.0.113."`
	// From and To are unix milliseconds, both inclusive; 0 leaves that side open.
	From int64 `json:"from" example:"1735689600000"`
	To   int64 `json:"to" example:"1735775999999"`
	// Before is the Next of the previous page; empty starts at the newest row.
	Before string `json:"before" example:"20250101:4096"`
	Limit  int    `json:"limit" example:"50"`
}

// ConnPage is one page of connections, newest first.
type ConnPage struct {
	Rows []ConnRecord `json:"rows"`
	// Next goes into ConnFilter.Before to read the following page; empty on the last one.
	Next string `json:"next" example:"20250101:4046"`
	// Enabled tells whether connections are being recorded now; SizeBytes is the log on disk.
	Enabled   bool  `json:"enabled" example:"true"`
	SizeBytes int64 `json:"sizeBytes" example:"36700160"`
}

// ErrBadCursor reports a ConnFilter.Before that no page handed out.
var ErrBadCursor = errors.New("connection log: invalid page cursor")

func parseCursor(cursor string) (day string, id int64, err error) {
	if cursor == "" {
		return "", 0, nil
	}
	day, rawID, ok := strings.Cut(cursor, ":")
	if _, dayErr := time.Parse(dayLayout, day); !ok || dayErr != nil {
		return "", 0, ErrBadCursor
	}
	if id, err = strconv.ParseInt(rawID, 10, 64); err != nil || id < 1 {
		return "", 0, ErrBadCursor
	}
	return day, id, nil
}

func likeContains(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s) + "%"
}

// Scan calls fn for up to limit matching connections, newest first, with the cursor that
// continues after each. It walks the day files backwards and opens only those in range.
func Scan(ctx context.Context, dir string, f ConnFilter, limit int, fn func(rec ConnRecord, cursor string) error) error {
	startDay, startID, err := parseCursor(f.Before)
	if err != nil {
		return err
	}
	// A row is never filed in an earlier day than the one written last: after a clock set back it sits in the next file.
	lastDay := ""
	if f.To > 0 {
		lastDay = time.UnixMilli(f.To).UTC().AddDate(0, 0, 1).Format(dayLayout)
	}
	list := days(dir)
	for i := len(list) - 1; i >= 0 && limit > 0; i-- {
		day := list[i]
		if (startDay != "" && day > startDay) || (lastDay != "" && day > lastDay) {
			continue
		}
		if f.From > 0 && day < dayOf(f.From) {
			break
		}
		before := int64(0)
		if day == startDay {
			before = startID
		}
		n, err := scanDay(ctx, dir, day, f, before, limit, fn)
		if err != nil {
			return err
		}
		limit -= n
	}
	return nil
}

func scanDay(ctx context.Context, dir, day string, f ConnFilter, before int64, limit int, fn func(ConnRecord, string) error) (int, error) {
	// mode=rw never creates a file: a day pruned a moment ago is skipped, not reborn empty.
	db, err := sql.Open("sqlite3", sqliteURI(dayPath(dir, day), "mode=rw&_query_only=1&_busy_timeout=5000"))
	if err != nil {
		return 0, err
	}
	defer db.Close()

	where, args := []string{"1"}, []any{}
	add := func(cond string, arg any) {
		where = append(where, cond)
		args = append(args, arg)
	}
	if before > 0 {
		add("c.id < ?", before)
	}
	if f.From > 0 {
		add("c.ts >= ?", f.From)
	}
	if f.To > 0 {
		add("c.ts <= ?", f.To)
	}
	if f.Client != "" {
		add("c.client = (SELECT id FROM str WHERE v = ?)", f.Client)
	}
	if f.Inbound != "" {
		add("c.inbound = (SELECT id FROM str WHERE v = ?)", f.Inbound)
	}
	if f.Host != "" {
		add(`c.dst IN (SELECT id FROM str WHERE v LIKE ? ESCAPE '\')`, likeContains(f.Host))
	}
	if f.Source != "" {
		add(`c.src IN (SELECT id FROM str WHERE v LIKE ? ESCAPE '\')`, likeContains(f.Source))
	}
	args = append(args, limit)
	// Strings are read back row by row, which leaves the planner one table to order: conn.
	const text = "COALESCE((SELECT v FROM str WHERE id = c.%s), '')"
	query := "SELECT c.id, c.ts, " + fmt.Sprintf(text, "client") + ", " + fmt.Sprintf(text, "inbound") + ", " +
		fmt.Sprintf(text, "outbound") + ", " + fmt.Sprintf(text, "src") + ", c.sport, c.net, " + fmt.Sprintf(text, "dst") +
		", c.dport FROM conn c WHERE " + strings.Join(where, " AND ") + " ORDER BY c.id DESC LIMIT ?"
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		// the day was pruned between the listing and the open
		if _, statErr := os.Stat(dayPath(dir, day)); errors.Is(statErr, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			rec     ConnRecord
			id      int64
			network int
		)
		if err := rows.Scan(&id, &rec.Time, &rec.Client, &rec.Inbound, &rec.Outbound, &rec.Source, &rec.SourcePort, &network, &rec.Host, &rec.Port); err != nil {
			return n, err
		}
		rec.Network = "tcp"
		if network == 1 {
			rec.Network = "udp"
		}
		if err := fn(rec, day+":"+strconv.FormatInt(id, 10)); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// Query returns one page of the log.
func Query(ctx context.Context, dir string, f ConnFilter) (*ConnPage, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultPageRows
	}
	limit = min(limit, MaxPageRows)
	page := &ConnPage{Rows: make([]ConnRecord, 0, limit)}
	var cursors []string
	// One row past the page tells whether another page exists.
	err := Scan(ctx, dir, f, limit+1, func(rec ConnRecord, cursor string) error {
		page.Rows = append(page.Rows, rec)
		cursors = append(cursors, cursor)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(page.Rows) > limit {
		page.Rows = page.Rows[:limit]
		page.Next = cursors[limit-1]
	}
	return page, nil
}
