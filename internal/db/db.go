// Package db wraps SQLite for the heat-aggregation and pull-history tables.
//
// Driver choice: modernc.org/sqlite (pure Go, no CGO). CGO-free means
// cross-compile to scratch, alpine, distroless, etc. without a C toolchain.
// Trade-off: ~30% slower than mattn/go-sqlite3 for write-heavy workloads,
// which is fine for our append-mostly insert/aggregate pattern.
//
// Schema migrations follow registry-manager's strict rule (AGENTS.md
// §"改 schema 的纪律"): SCHEMA_VERSION +1 and the if (current < N) block
// must land in the SAME commit. Half-applied versions permanently brick
// the DB on next startup.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// SCHEMA_VERSION is bumped together with new migrations.
// Bump rule: +1 per migration; never reuse a number; never delete a migration.
const SCHEMA_VERSION = 1

// Db is the SQLite wrapper. All exported methods are safe for concurrent use.
type Db struct {
	conn *sql.DB
	path string
}

// Open opens (or creates) the SQLite database at path and runs pending migrations.
//
// On startup the file's user_version pragma is read; migrations with version
// > current are applied in order inside a transaction.
func Open(path string) (*Db, error) {
	if err := ensureParent(path); err != nil {
		return nil, err
	}
	// _journal=WAL lets readers proceed while a writer is in flight;
	// _busy_timeout=5s avoids spurious SQLITE_BUSY under modest concurrency.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	d := &Db{conn: conn, path: path}
	if err := d.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("db: migrate: %w", err)
	}
	return d, nil
}

// Close flushes WAL and closes the connection.
func (d *Db) Close() error { return d.conn.Close() }

// Path returns the file path the DB was opened with.
func (d *Db) Path() string { return d.path }

// migrate applies pending schema migrations. Migrations are append-only;
// re-running an old migration must be a no-op (every CREATE TABLE uses
// IF NOT EXISTS, every INSERT uses INSERT OR IGNORE / ON CONFLICT).
func (d *Db) migrate() error {
	current, err := d.userVersion()
	if err != nil {
		return err
	}
	for v := current + 1; v <= SCHEMA_VERSION; v++ {
		body, ok := migrations[v]
		if !ok {
			return fmt.Errorf("db: missing migration for version %d", v)
		}
		if err := d.execMigration(v, body); err != nil {
			return err
		}
	}
	return nil
}

func (d *Db) userVersion() (int, error) {
	var v int
	if err := d.conn.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

func (d *Db) execMigration(v int, body string) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(body); err != nil {
		return fmt.Errorf("migration %d: %w", v, err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		return err
	}
	return tx.Commit()
}

// migrations holds version → SQL body. Append-only; never edit a published migration.
var migrations = map[int]string{
	1: `
	-- Daily heat aggregation: one row per (day, repo, tag, action).
	-- Used by the calendar / top repos queries.
	CREATE TABLE IF NOT EXISTS activity_daily (
		day         TEXT NOT NULL,           -- 'YYYY-MM-DD' UTC
		repository  TEXT NOT NULL,           -- 'library/alpine'
		tag         TEXT NOT NULL DEFAULT '',-- '3.19' or '' for tag-less events
		action      TEXT NOT NULL,           -- 'pull' | 'push' (whitelist)
		count       INTEGER NOT NULL DEFAULT 0,
		bytes       INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (day, repository, tag, action)
	) WITHOUT ROWID;
	CREATE INDEX IF NOT EXISTS activity_daily_day ON activity_daily(day);

	-- Pull job history (terminal states only; running/queued live in memory).
	CREATE TABLE IF NOT EXISTS pull_jobs (
		id          TEXT PRIMARY KEY,
		source_ref  TEXT NOT NULL,
		dest_repo   TEXT NOT NULL,
		dest_tag    TEXT NOT NULL,
		state       TEXT NOT NULL,           -- succeeded | failed | cancelled
		error       TEXT NOT NULL DEFAULT '',
		bytes_done  INTEGER NOT NULL DEFAULT 0,
		bytes_total INTEGER NOT NULL DEFAULT 0,
		started_at  TEXT NOT NULL,           -- RFC3339 UTC
		ended_at    TEXT NOT NULL,
		created_at  TEXT NOT NULL
	) WITHOUT ROWID;
	CREATE INDEX IF NOT EXISTS pull_jobs_started ON pull_jobs(started_at);
	`,
}

// ActivityIncrement applies one heat increment. Used by events.aggregator.
//
// Both count and bytes are summed so the calendar totals match the sum of
// individual events. The PRIMARY KEY conflict is the upsert contract; we
// ON CONFLICT add the increment so concurrent writers compose correctly.
func (d *Db) ActivityIncrement(ctx context.Context, day, repo, tag, action string, count, bytes int64) error {
	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO activity_daily(day, repository, tag, action, count, bytes)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(day, repository, tag, action) DO UPDATE SET
			count = count + excluded.count,
			bytes = bytes + excluded.bytes
	`, day, repo, tag, action, count, bytes)
	return err
}

// PullJobRecord persists a terminal pull job. Running jobs stay in memory.
func (d *Db) PullJobRecord(ctx context.Context, j PullJobRow) error {
	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO pull_jobs(id, source_ref, dest_repo, dest_tag, state, error,
		                     bytes_done, bytes_total, started_at, ended_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			state = excluded.state,
			error = excluded.error,
			bytes_done = excluded.bytes_done,
			bytes_total = excluded.bytes_total,
			ended_at = excluded.ended_at
	`, j.ID, j.SourceRef, j.DestRepo, j.DestTag, j.State, j.Error,
		j.BytesDone, j.BytesTotal,
		j.StartedAt.UTC().Format(time.RFC3339),
		j.EndedAt.UTC().Format(time.RFC3339),
		j.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

// PullJobRow is the SQL-side view of a pull job. Mirrors pull.Job but
// without the mutex / cancelFn (which aren't meaningful on disk).
type PullJobRow struct {
	ID         string
	SourceRef  string
	DestRepo   string
	DestTag    string
	State      string
	Error      string
	BytesDone  int64
	BytesTotal int64
	StartedAt  time.Time
	EndedAt    time.Time
	CreatedAt  time.Time
}

// ActivitySummary is the JSON shape returned by /api/stats/summary.
type ActivitySummary struct {
	TotalPulls  int64     `json:"totalPulls"`
	TotalPushes int64     `json:"totalPushes"`
	UniqueRepos int       `json:"uniqueRepos"`
	FirstSeen   time.Time `json:"firstSeen,omitempty"`
	LastSeen    time.Time `json:"lastSeen,omitempty"`
}

// GetSummary returns aggregated totals for the dashboard header card.
func (d *Db) GetSummary(ctx context.Context, since time.Time) (ActivitySummary, error) {
	var s ActivitySummary
	row := d.conn.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN action='pull' THEN count ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN action='push' THEN count ELSE 0 END), 0),
			COUNT(DISTINCT repository),
			MIN(day),
			MAX(day)
		FROM activity_daily
		WHERE day >= ?
	`, since.UTC().Format("2006-01-02"))
	var minDay, maxDay sql.NullString
	if err := row.Scan(&s.TotalPulls, &s.TotalPushes, &s.UniqueRepos, &minDay, &maxDay); err != nil {
		return s, err
	}
	if minDay.Valid {
		s.FirstSeen, _ = time.Parse("2006-01-02", minDay.String)
	}
	if maxDay.Valid {
		s.LastSeen, _ = time.Parse("2006-01-02", maxDay.String)
	}
	return s, nil
}

// TopRepo is one entry in the top-repos view.
type TopRepo struct {
	Repo       string  `json:"repo"`
	Pulls      int64   `json:"pulls"`
	Pushes     int64   `json:"pushes"`
	BytesTotal int64   `json:"bytesTotal"`
}

// GetTopRepos returns the busiest repositories in the window.
func (d *Db) GetTopRepos(ctx context.Context, since time.Time, limit int) ([]TopRepo, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.conn.QueryContext(ctx, `
		SELECT repository,
		       COALESCE(SUM(CASE WHEN action='pull' THEN count ELSE 0 END), 0) AS pulls,
		       COALESCE(SUM(CASE WHEN action='push' THEN count ELSE 0 END), 0) AS pushes,
		       COALESCE(SUM(bytes), 0) AS bytes
		FROM activity_daily
		WHERE day >= ?
		GROUP BY repository
		ORDER BY pulls + pushes DESC
		LIMIT ?
	`, since.UTC().Format("2006-01-02"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopRepo
	for rows.Next() {
		var r TopRepo
		if err := rows.Scan(&r.Repo, &r.Pulls, &r.Pushes, &r.BytesTotal); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActivityPoint is one cell of the calendar heatmap.
type ActivityPoint struct {
	Day      string `json:"day"`      // 'YYYY-MM-DD'
	Repo     string `json:"repo"`
	Tag      string `json:"tag"`
	Action   string `json:"action"`
	Count    int64  `json:"count"`
	BytesOut int64  `json:"bytes"`
}

// GetSeries returns per-(day, repo, tag, action) cells for the heatmap.
// limit caps the result so a wide repo doesn't blow up the JSON payload.
func (d *Db) GetSeries(ctx context.Context, since time.Time, limit int) ([]ActivityPoint, error) {
	if limit <= 0 {
		limit = 5000
	}
	rows, err := d.conn.QueryContext(ctx, `
		SELECT day, repository, tag, action, count, bytes
		FROM activity_daily
		WHERE day >= ?
		ORDER BY day DESC, (count + bytes) DESC
		LIMIT ?
	`, since.UTC().Format("2006-01-02"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityPoint
	for rows.Next() {
		var p ActivityPoint
		if err := rows.Scan(&p.Day, &p.Repo, &p.Tag, &p.Action, &p.Count, &p.BytesOut); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RetentionCleanup deletes activity older than cutoff (days). Idempotent.
func (d *Db) RetentionCleanup(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := d.conn.ExecContext(ctx, `
		DELETE FROM activity_daily WHERE day < ?
	`, cutoff.UTC().Format("2006-01-02"))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func ensureParent(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}