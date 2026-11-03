// Package archive moves history older than 12 months out of Postgres into
// compressed files in object storage (plan §8.2 "archiver", §8.3), so the
// database stays small while nothing is deleted (D18, D26):
//
//   - each monthly partition of the time-series tables (snapshots, comment
//     versions, commenter profiles) becomes gzip JSON-lines parts, one line
//     per row: {"row": {...the row...}, "ids": {"channel": …, "author": …}};
//   - every part is read back and checked (checksum and row count) before
//     the partition is dropped from Postgres;
//   - erasures keep working: rows of anything on the suppression list are
//     left out when a month is archived, and a scrub rewrites parts that
//     hold rows erased later (the "ids" say whose data a row is, even after
//     the database rows that linked them are gone);
//   - the archive stays searchable (Search, by ID) and a month can be put
//     back into Postgres for a while (Restore).
//
// The parts are plain JSON lines, so ClickHouse or DuckDB can query them in
// place later if needed.
package archive

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
)

// Defaults.
const (
	AfterMonths = 12        // a month is archived once it ended this long ago
	PartRows    = 500_000   // rows per part at most
	PartBytes   = 256 << 20 // uncompressed bytes per part at most
	lockTimeout = "10s"     // waiting for the table lock to drop a partition
	maxLine     = 64 << 20  // longest line read back
	// scrubMargin: a part counts as scrubbed up to this long before the
	// suppression list was read (a request committing at that moment is
	// caught by the next scrub, never missed).
	scrubMargin = 10 * time.Minute
)

// dbNow is the database's clock (suppression times come from it).
func dbNow(ctx context.Context, q db.Querier) (time.Time, error) {
	var t time.Time
	err := q.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&t)
	return t, err
}

// spec says how a table's rows are written: the line for each row, with
// the IDs whose erasure removes it.
type spec struct {
	order string // a stable order (the primary key)
	from  string // joins that find the IDs
	ids   string // jsonb_build_object arguments
}

var specs = map[string]spec{
	"channel_snapshots": {order: "t.channel_id, t.taken_at", ids: `'channel', t.channel_id`},
	"video_snapshots": {order: "t.video_id, t.taken_at", from: `LEFT JOIN videos v ON v.id = t.video_id`,
		ids: `'video', t.video_id, 'channel', v.channel_id`},
	"post_snapshots": {order: "t.post_id, t.taken_at", from: `LEFT JOIN posts p ON p.id = t.post_id`,
		ids: `'post', t.post_id, 'channel', p.channel_id`},
	"comment_versions": {order: "t.comment_id, t.version, t.seen_at", from: `LEFT JOIN comments c ON c.id = t.comment_id`,
		ids: `'comment', t.comment_id, 'author', c.author_id, 'channel', c.channel_id, COALESCE(c.parent_kind, 'video'), c.parent_id`},
	"author_profiles": {order: "t.author_id, t.fetched_at", ids: `'author', t.author_id`},
}

// Tables lists the archived tables.
func Tables() []string {
	out := make([]string, 0, len(db.Partitioned))
	for _, p := range db.Partitioned {
		out = append(out, p.Table)
	}
	return out
}

// Archiver moves old partitions into the store.
type Archiver struct {
	Pool        *pgxpool.Pool
	Store       media.Store
	Sup         *privacy.Suppressor
	Log         *slog.Logger
	Now         func() time.Time
	AfterMonths int // default AfterMonths
	PartRows    int // default PartRows
	PartBytes   int // default PartBytes
}

func (a *Archiver) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Archiver) after() int {
	if a.AfterMonths > 0 {
		return a.AfterMonths
	}
	return AfterMonths
}

// Key is the object key of a part.
func Key(table string, month time.Time, part int) string {
	return fmt.Sprintf("archive/%s/%s/part-%04d.jsonl.gz", table, month.Format("2006-01"), part)
}

var partitionName = regexp.MustCompile(`_y(\d{4})m(\d{2})$`)

// Due lists the months of table that are old enough to archive: old
// partitions, and old months still in the default partition.
func (a *Archiver) Due(ctx context.Context, table string) ([]time.Time, error) {
	col := column(table)
	if col == "" {
		return nil, fmt.Errorf("archive: %s is not an archived table", table)
	}
	cut := a.now().UTC().AddDate(0, -a.after(), 0)
	eligible := func(m time.Time) bool { return !m.AddDate(0, 1, 0).After(cut) }
	seen := map[time.Time]bool{}
	var out []time.Time
	// Old rows in the default partition get their own partition first.
	var oldest *time.Time
	if err := a.Pool.QueryRow(ctx, fmt.Sprintf(`SELECT min(%s) FROM %s_default`, col, table)).Scan(&oldest); err != nil {
		return nil, err
	}
	if oldest != nil {
		for m := monthOf(*oldest); eligible(m); m = m.AddDate(0, 1, 0) {
			var n int
			if err := a.Pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM %s_default WHERE %s >= $1 AND %s < $2 LIMIT 1) x`,
				table, col, col), m, m.AddDate(0, 1, 0)).Scan(&n); err != nil {
				return nil, err
			}
			if n > 0 {
				if err := db.EnsureMonth(ctx, a.Pool, table, m); err != nil {
					return nil, err
				}
			}
		}
	}
	rows, err := a.Pool.Query(ctx, `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE p.relname = $1 AND n.nspname = current_schema()`, table)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		mm := partitionName.FindStringSubmatch(name)
		if mm == nil {
			continue
		}
		var y, mo int
		fmt.Sscan(mm[1], &y)
		fmt.Sscan(mm[2], &mo)
		m := time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC)
		if !eligible(m) || seen[m] {
			continue
		}
		var until *time.Time
		err := a.Pool.QueryRow(ctx, `SELECT restored_until FROM archive_months WHERE table_name = $1 AND month = $2`, table, m).Scan(&until)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if until != nil && until.After(a.now()) {
			continue // restored by an admin for a while
		}
		seen[m] = true
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out, nil
}

func monthOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func column(table string) string {
	for _, p := range db.Partitioned {
		if p.Table == table {
			return p.Column
		}
	}
	return ""
}

// Result is what archiving one month did.
type Result struct {
	Table   string
	Month   time.Time
	Rows    int64 // written to the archive
	Skipped int64 // rows of erased items, left out
	Parts   int
	Bytes   int64 // compressed
}

// Run archives every due month of every table.
func (a *Archiver) Run(ctx context.Context) ([]Result, error) {
	var out []Result
	for _, table := range Tables() {
		months, err := a.Due(ctx, table)
		if err != nil {
			return out, err
		}
		for _, m := range months {
			r, err := a.Month(ctx, table, m)
			if err != nil {
				return out, fmt.Errorf("archive %s %s: %w", table, m.Format("2006-01"), err)
			}
			out = append(out, r)
			a.Log.Info("month archived", "table", table, "month", m.Format("2006-01"), "rows", r.Rows, "skipped_erased", r.Skipped,
				"parts", r.Parts, "bytes", r.Bytes)
		}
	}
	return out, nil
}

var newline = []byte{'\n'}

type lineIDs struct {
	IDs map[string]string `json:"ids"`
}

// erased reports whether a line belongs to anything on the suppression list.
func (a *Archiver) erased(line []byte) (bool, error) {
	var l lineIDs
	if err := json.Unmarshal(line, &l); err != nil {
		return false, fmt.Errorf("archive: a line is not JSON: %w", err)
	}
	for kind, id := range l.IDs {
		if a.Sup.Suppressed(kind, id) {
			return true, nil
		}
	}
	return false, nil
}

type part struct {
	n     int
	rows  int64
	raw   int
	buf   bytes.Buffer
	gz    *gzip.Writer
	saved []savedPart
}

type savedPart struct {
	n     int
	rows  int64
	bytes int64
	sum   string
}

// Month archives one month of table: the parts are written and checked,
// then the partition is dropped.
func (a *Archiver) Month(ctx context.Context, table string, m time.Time) (Result, error) {
	sp, ok := specs[table]
	if !ok {
		return Result{}, fmt.Errorf("archive: %s is not an archived table", table)
	}
	m = monthOf(m)
	res := Result{Table: table, Month: m}
	name := db.PartitionName(table, m)
	partRows, partBytes := a.PartRows, a.PartBytes
	if partRows <= 0 {
		partRows = PartRows
	}
	if partBytes <= 0 {
		partBytes = PartBytes
	}
	// A month archived before (late rows came in) gets parts added after the
	// ones it has; otherwise (first time, a crashed attempt, or a month that
	// was restored) its parts are written from 1.
	var archived bool
	var restored *time.Time
	err := a.Pool.QueryRow(ctx, `SELECT restored_until FROM archive_months WHERE table_name = $1 AND month = $2`, table, m).Scan(&restored)
	switch {
	case err == nil:
		archived = true
	case !errors.Is(err, pgx.ErrNoRows):
		return res, err
	}
	first := 1
	if archived && restored == nil {
		if err := a.Pool.QueryRow(ctx, `SELECT COALESCE(max(part), 0) + 1 FROM archive_index WHERE table_name = $1 AND month = $2`,
			table, m).Scan(&first); err != nil {
			return res, err
		}
	}

	var expected int64
	p := &part{n: first}
	newPart := func() {
		p.buf.Reset()
		p.gz = gzip.NewWriter(&p.buf)
		p.rows, p.raw = 0, 0
	}
	newPart()
	flush := func() error {
		if p.rows == 0 {
			return nil
		}
		if err := p.gz.Close(); err != nil {
			return err
		}
		data := p.buf.Bytes()
		sum := sha256.Sum256(data)
		if err := a.Store.Put(ctx, Key(table, m, p.n), "application/gzip", data); err != nil {
			return err
		}
		p.saved = append(p.saved, savedPart{n: p.n, rows: p.rows, bytes: int64(len(data)), sum: hex.EncodeToString(sum[:])})
		p.n++
		newPart()
		return nil
	}

	// Pass 1, under a share lock (reads go on, writes to this month wait):
	// write the parts, leaving out erased rows.
	var scrubbedAt time.Time
	err = pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`LOCK TABLE %s IN SHARE MODE`, name)); err != nil {
			return err
		}
		t, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		scrubbedAt = t.Add(-scrubMargin)
		if err := a.Sup.Refresh(ctx, tx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, name)).Scan(&expected); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT jsonb_build_object('row', to_jsonb(t), 'ids', jsonb_strip_nulls(jsonb_build_object(%s)))::text
			FROM %s t %s ORDER BY %s`, sp.ids, name, sp.from, sp.order))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			gone, err := a.erased([]byte(line))
			if err != nil {
				return err
			}
			if gone {
				res.Skipped++
				continue
			}
			if _, err := p.gz.Write([]byte(line + "\n")); err != nil {
				return err
			}
			p.rows++
			p.raw += len(line) + 1
			res.Rows++
			if p.rows >= int64(partRows) || p.raw >= partBytes {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return flush()
	})
	if err != nil {
		return res, err
	}
	if res.Rows+res.Skipped != expected {
		return res, fmt.Errorf("archive: wrote %d and left out %d rows of %d", res.Rows, res.Skipped, expected)
	}

	// Pass 2: read every part back and check it before anything is dropped.
	for _, s := range p.saved {
		n, sum, err := a.check(ctx, Key(table, m, s.n))
		if err != nil {
			return res, err
		}
		if n != s.rows || sum != s.sum {
			return res, fmt.Errorf("archive: part %d reads back as %d rows (wrote %d) or a different checksum", s.n, n, s.rows)
		}
		res.Parts++
		res.Bytes += s.bytes
	}

	// Pass 3: record the parts and drop the partition, in one transaction.
	var stale []string
	err = pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '`+lockTimeout+`'`); err != nil {
			return err
		}
		for _, s := range p.saved {
			if _, err := tx.Exec(ctx, `INSERT INTO archive_index (table_name, month, part, object_key, rows, bytes, sha256, scrubbed_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				ON CONFLICT (table_name, month, part) DO UPDATE SET object_key = EXCLUDED.object_key, rows = EXCLUDED.rows,
					bytes = EXCLUDED.bytes, sha256 = EXCLUDED.sha256, created_at = now(), scrubbed_at = EXCLUDED.scrubbed_at`,
				table, m, s.n, Key(table, m, s.n), s.rows, s.bytes, s.sum, scrubbedAt); err != nil {
				return err
			}
		}
		last := first + len(p.saved) - 1
		if first == 1 {
			// Parts beyond the ones just written are from an earlier,
			// longer attempt: their rows are all in the new parts.
			rows, err := tx.Query(ctx, `DELETE FROM archive_index WHERE table_name = $1 AND month = $2 AND part > $3 RETURNING object_key`,
				table, m, last)
			if err != nil {
				return err
			}
			if stale, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
				return err
			}
		}
		// Rows written since pass 1 would be lost by the drop: check.
		var now int64
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, name)).Scan(&now); err != nil {
			return err
		}
		if now > expected {
			return fmt.Errorf("archive: %d rows were added to %s while it was archived; it is tried again next time", now-expected, name)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO archive_months (table_name, month, rows, parts, archived_at)
			SELECT $1, $2, sum(rows), count(*), now() FROM archive_index WHERE table_name = $1 AND month = $2
			ON CONFLICT (table_name, month) DO UPDATE SET rows = EXCLUDED.rows, parts = EXCLUDED.parts,
				archived_at = now(), restored_until = NULL`, table, m); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s DETACH PARTITION %s`, table, name)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, fmt.Sprintf(`DROP TABLE %s`, name))
		return err
	})
	if err != nil {
		return res, err
	}
	for _, k := range stale {
		if err := a.Store.Delete(ctx, k); err != nil {
			a.Log.Warn("deleting a superseded archive part failed", "key", k, "err", err)
		}
	}
	return res, nil
}

// read opens a part: its lines, the row count and the checksum of the
// stored bytes.
func (a *Archiver) read(ctx context.Context, key string, each func(line []byte) error) (int64, string, error) {
	data, err := a.Store.Get(ctx, key)
	if err != nil {
		return 0, "", err
	}
	sum := sha256.Sum256(data)
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return 0, "", fmt.Errorf("archive: %s is not gzip: %w", key, err)
	}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 1<<20), maxLine)
	var n int64
	for sc.Scan() {
		n++
		if each != nil {
			if err := each(sc.Bytes()); err != nil {
				return n, "", err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return n, "", fmt.Errorf("archive: reading %s: %w", key, err)
	}
	return n, hex.EncodeToString(sum[:]), nil
}

func (a *Archiver) check(ctx context.Context, key string) (int64, string, error) {
	return a.read(ctx, key, nil)
}

// Part is one archived file.
type Part struct {
	Table      string
	Month      time.Time
	Part       int
	Key        string
	Rows       int64
	Bytes      int64
	SHA256     string
	ScrubbedAt time.Time
}

func (a *Archiver) parts(ctx context.Context, where string, args ...any) ([]Part, error) {
	rows, err := a.Pool.Query(ctx, `SELECT table_name, month, part, object_key, rows, bytes, sha256, scrubbed_at
		FROM archive_index WHERE `+where+` ORDER BY table_name, month, part`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Part, error) {
		var p Part
		err := r.Scan(&p.Table, &p.Month, &p.Part, &p.Key, &p.Rows, &p.Bytes, &p.SHA256, &p.ScrubbedAt)
		return p, err
	})
}

// List lists the archived parts.
func (a *Archiver) List(ctx context.Context) ([]Part, error) { return a.parts(ctx, "true") }

// Scrub applies erasures made after a part was written: parts holding rows
// of items now on the suppression list are rewritten without them. It
// returns the rows removed.
func (a *Archiver) Scrub(ctx context.Context) (int64, error) {
	var latest *time.Time
	if err := a.Pool.QueryRow(ctx, `SELECT max(created_at) FROM suppression_list`).Scan(&latest); err != nil || latest == nil {
		return 0, err
	}
	todo, err := a.parts(ctx, "scrubbed_at <= $1", *latest)
	if err != nil || len(todo) == 0 {
		return 0, err
	}
	t, err := dbNow(ctx, a.Pool)
	if err != nil {
		return 0, err
	}
	start := t.Add(-scrubMargin)
	if err := a.Sup.Refresh(ctx, a.Pool); err != nil {
		return 0, err
	}
	var total int64
	for _, p := range todo {
		var keep bytes.Buffer
		zw := gzip.NewWriter(&keep)
		var dropped, kept int64
		_, sum, err := a.read(ctx, p.Key, func(line []byte) error {
			gone, err := a.erased(line)
			if err != nil {
				return err
			}
			if gone {
				dropped++
				return nil
			}
			kept++
			if _, err := zw.Write(line); err != nil {
				return err
			}
			_, err = zw.Write(newline)
			return err
		})
		if err != nil {
			return total, err
		}
		if sum != p.SHA256 {
			return total, fmt.Errorf("archive: %s does not match its checksum; not rewritten", p.Key)
		}
		if dropped > 0 {
			if err := zw.Close(); err != nil {
				return total, err
			}
			data := keep.Bytes()
			s := sha256.Sum256(data)
			if err := a.Store.Put(ctx, p.Key, "application/gzip", data); err != nil {
				return total, err
			}
			if _, err := a.Pool.Exec(ctx, `UPDATE archive_index SET rows = $4, bytes = $5, sha256 = $6
				WHERE table_name = $1 AND month = $2 AND part = $3`, p.Table, p.Month, p.Part, kept, len(data), hex.EncodeToString(s[:])); err != nil {
				return total, err
			}
			if _, err := a.Pool.Exec(ctx, `UPDATE archive_months SET rows = rows - $3 WHERE table_name = $1 AND month = $2`,
				p.Table, p.Month, dropped); err != nil {
				return total, err
			}
			_, _ = a.Pool.Exec(ctx, `INSERT INTO erasure_audit (request_id, step, rows) VALUES (0, $1, $2)`,
				"archive "+p.Table+" "+p.Month.Format("2006-01"), dropped)
			total += dropped
		}
		if _, err := a.Pool.Exec(ctx, `UPDATE archive_index SET scrubbed_at = $4 WHERE table_name = $1 AND month = $2 AND part = $3`,
			p.Table, p.Month, p.Part, start); err != nil {
			return total, err
		}
	}
	if total > 0 {
		a.Log.Info("erased rows removed from the archive", "rows", total)
	}
	return total, nil
}

// Query selects archived lines: a table, a month range (zero: all) and
// IDs that must all match (kind → ID, for example "channel" → "UC…").
type Query struct {
	Table    string
	From, To time.Time // months, inclusive
	IDs      map[string]string
}

// Search writes the matching archived lines to out and returns how many.
// Rows of erased items are never shown, even before the scrub has run.
func (a *Archiver) Search(ctx context.Context, q Query, out io.Writer) (int64, error) {
	if column(q.Table) == "" {
		return 0, fmt.Errorf("archive: %s is not an archived table (%s)", q.Table, strings.Join(Tables(), ", "))
	}
	if err := a.Sup.Refresh(ctx, a.Pool); err != nil {
		return 0, err
	}
	where, args := "table_name = $1", []any{q.Table}
	if !q.From.IsZero() {
		args = append(args, monthOf(q.From))
		where += fmt.Sprintf(" AND month >= $%d", len(args))
	}
	if !q.To.IsZero() {
		args = append(args, monthOf(q.To))
		where += fmt.Sprintf(" AND month <= $%d", len(args))
	}
	parts, err := a.parts(ctx, where, args...)
	if err != nil {
		return 0, err
	}
	var found int64
	for _, p := range parts {
		_, _, err := a.read(ctx, p.Key, func(line []byte) error {
			var l lineIDs
			if err := json.Unmarshal(line, &l); err != nil {
				return err
			}
			for k, v := range q.IDs {
				if l.IDs[k] != v {
					return nil
				}
			}
			for kind, id := range l.IDs {
				if a.Sup.Suppressed(kind, id) {
					return nil
				}
			}
			found++
			if _, err := out.Write(line); err != nil {
				return err
			}
			_, err := out.Write(newline)
			return err
		})
		if err != nil {
			return found, err
		}
	}
	return found, nil
}

// Restore puts an archived month back into Postgres until the given time
// (the archiver moves it out again after that). Erased rows are left out.
// It returns the rows restored.
func (a *Archiver) Restore(ctx context.Context, table string, m time.Time, until time.Time) (int64, error) {
	if column(table) == "" {
		return 0, fmt.Errorf("archive: %s is not an archived table", table)
	}
	m = monthOf(m)
	var n int
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM archive_months WHERE table_name = $1 AND month = $2`, table, m).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, fmt.Errorf("archive: %s %s is not archived", table, m.Format("2006-01"))
	}
	if err := a.Sup.Refresh(ctx, a.Pool); err != nil {
		return 0, err
	}
	if err := db.EnsureMonth(ctx, a.Pool, table, m); err != nil {
		return 0, err
	}
	parts, err := a.parts(ctx, "table_name = $1 AND month = $2", table, m)
	if err != nil {
		return 0, err
	}
	var restored int64
	insert := fmt.Sprintf(`INSERT INTO %s SELECT * FROM jsonb_populate_record(NULL::%s, $1::jsonb -> 'row') ON CONFLICT DO NOTHING`, table, table)
	for _, p := range parts {
		batch := &pgx.Batch{}
		send := func() error {
			if batch.Len() == 0 {
				return nil
			}
			err := a.Pool.SendBatch(ctx, batch).Close()
			batch = &pgx.Batch{}
			return err
		}
		_, _, err := a.read(ctx, p.Key, func(line []byte) error {
			gone, err := a.erased(line)
			if err != nil || gone {
				return err
			}
			batch.Queue(insert, string(line))
			restored++
			if batch.Len() >= 1000 {
				return send()
			}
			return nil
		})
		if err != nil {
			return restored, err
		}
		if err := send(); err != nil {
			return restored, err
		}
	}
	_, err = a.Pool.Exec(ctx, `UPDATE archive_months SET restored_until = $3 WHERE table_name = $1 AND month = $2`, table, m, until)
	return restored, err
}
