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
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SCHEMA_VERSION is bumped together with new migrations.
// Bump rule: +1 per migration; never reuse a number; never delete a migration.
const SCHEMA_VERSION = 3

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
	2: `
	-- Panel-managed User-Agent ignore rules (settings page).
	-- Env-provided rules (REGISTRY_STATS_IGNORE_USERAGENTS) are NOT stored
	-- here; the effective list is the union of env + panel at runtime.
	CREATE TABLE IF NOT EXISTS stats_ignore (
		useragent  TEXT PRIMARY KEY,
		created_at TEXT NOT NULL
	) WITHOUT ROWID;
	`,
	3: `
	-- v0.5.1: runtime-mutable settings (key/value JSON-encoded strings).
	-- Currently used for the default upstream URL -- env REGISTRY_URL is
	-- still the bootstrap default but operators can edit it on the
	-- settings page from then on. Keys are dotted namespaces:
	--   registry.url           default upstream URL ("" == Docker Hub)
	CREATE TABLE IF NOT EXISTS settings (
		key        TEXT PRIMARY KEY,
		value      TEXT NOT NULL,
		updated_at INTEGER NOT NULL        -- unix seconds
	) WITHOUT ROWID;
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
	UniqueTags  int       `json:"uniqueTags"`
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
			COUNT(DISTINCT repository || ':' || tag),
			MIN(day),
			MAX(day)
		FROM activity_daily
		WHERE day >= ?
	`, since.UTC().Format("2006-01-02"))
	var minDay, maxDay sql.NullString
	if err := row.Scan(&s.TotalPulls, &s.TotalPushes, &s.UniqueRepos, &s.UniqueTags, &minDay, &maxDay); err != nil {
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
	Repo       string `json:"repo"`
	Pulls      int64  `json:"pulls"`
	Pushes     int64  `json:"pushes"`
	BytesTotal int64  `json:"bytesTotal"`
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
	Day      string `json:"day"` // 'YYYY-MM-DD'
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

// PurgeAll deletes every heat row (settings page "clear heat data").
// Idempotent; returns the number of activity_daily rows removed.
//
// There is no event_seen table in this schema (dedup lives in the events
// ring buffer), so the API layer reports seen=0 alongside this count.
func (d *Db) PurgeAll(ctx context.Context) (int64, error) {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM activity_daily`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetFirstDay returns the earliest day present in activity_daily, or ""
// when the table is empty. Used for /api/config statsSince (all-time
// first day, independent of any query window).
func (d *Db) GetFirstDay(ctx context.Context) (string, error) {
	var day sql.NullString
	if err := d.conn.QueryRowContext(ctx, `SELECT MIN(day) FROM activity_daily`).Scan(&day); err != nil {
		return "", err
	}
	return day.String, nil
}

// TopItem is one entry of the /api/stats/top list. Tag is only set when
// aggregating by tag; Tags (distinct tag count) only when aggregating by
// repository — matching the UI's StatsTopItem optional fields.
type TopItem struct {
	Repository string  `json:"repository"`
	Tag        string  `json:"tag,omitempty"`
	Events     int64   `json:"events"`
	Pull       int64   `json:"pull"`
	Push       int64   `json:"push"`
	Tags       int     `json:"tags,omitempty"`
	LastAt     *string `json:"lastAt"`
}

// GetTop aggregates the busiest repositories (byTag=false) or individual
// tags (byTag=true) inside the window, hottest first.
func (d *Db) GetTop(ctx context.Context, since time.Time, limit int, byTag bool) ([]TopItem, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT repository,
		       COALESCE(SUM(count), 0),
		       COALESCE(SUM(CASE WHEN action='pull' THEN count ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN action='push' THEN count ELSE 0 END), 0),
		       COUNT(DISTINCT CASE WHEN tag <> '' THEN tag END),
		       MAX(day)
		FROM activity_daily
		WHERE day >= ?
		GROUP BY repository
		ORDER BY SUM(count) DESC, repository ASC
		LIMIT ?`
	if byTag {
		query = `
		SELECT repository || ':' || tag,
		       COALESCE(SUM(count), 0),
		       COALESCE(SUM(CASE WHEN action='pull' THEN count ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN action='push' THEN count ELSE 0 END), 0),
		       0,
		       MAX(day)
		FROM activity_daily
		WHERE day >= ? AND tag <> ''
		GROUP BY repository, tag
		ORDER BY SUM(count) DESC, repository ASC, tag ASC
		LIMIT ?`
	}
	rows, err := d.conn.QueryContext(ctx, query, since.UTC().Format("2006-01-02"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopItem{}
	for rows.Next() {
		var it TopItem
		var maxDay sql.NullString
		if byTag {
			// repository column carries "repo:tag"; split on the last colon
			// so repository names containing ':' (impossible per OCI spec)
			// still behave sanely.
			var combined string
			if err := rows.Scan(&combined, &it.Events, &it.Pull, &it.Push, new(int), &maxDay); err != nil {
				return nil, err
			}
			if i := strings.LastIndex(combined, ":"); i >= 0 {
				it.Repository, it.Tag = combined[:i], combined[i+1:]
			} else {
				it.Repository = combined
			}
		} else {
			if err := rows.Scan(&it.Repository, &it.Events, &it.Pull, &it.Push, &it.Tags, &maxDay); err != nil {
				return nil, err
			}
		}
		if maxDay.Valid {
			v := maxDay.String
			it.LastAt = &v
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// DayPoint is one day of the /api/stats/series trend.
type DayPoint struct {
	Day    string `json:"day"`
	Events int64  `json:"events"`
	Pull   int64  `json:"pull"`
	Push   int64  `json:"push"`
}

// GetSeriesByDay returns per-day totals (ascending), optionally filtered
// to a single repository. Days without events are simply absent — the UI
// renders gaps itself.
func (d *Db) GetSeriesByDay(ctx context.Context, since time.Time, repo string) ([]DayPoint, error) {
	query := `
		SELECT day,
		       COALESCE(SUM(count), 0),
		       COALESCE(SUM(CASE WHEN action='pull' THEN count ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN action='push' THEN count ELSE 0 END), 0)
		FROM activity_daily
		WHERE day >= ?`
	args := []any{since.UTC().Format("2006-01-02")}
	if repo != "" {
		query += ` AND repository = ?`
		args = append(args, repo)
	}
	query += ` GROUP BY day ORDER BY day ASC`
	rows, err := d.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DayPoint{}
	for rows.Next() {
		var p DayPoint
		if err := rows.Scan(&p.Day, &p.Events, &p.Pull, &p.Push); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RepoStat is one repository's heat inside the window, used to build the
// /api/stats/repositories map (the images page joins it onto the list).
type RepoStat struct {
	Repository string  `json:"repository"`
	Events     int64   `json:"events"`
	Pull       int64   `json:"pull"`
	Push       int64   `json:"push"`
	LastAt     *string `json:"lastAt"`
}

// GetRepoStats returns per-repository totals for every repository that has
// heat in the window. No limit: row count is bounded by the repository count.
func (d *Db) GetRepoStats(ctx context.Context, since time.Time) ([]RepoStat, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT repository,
		       COALESCE(SUM(count), 0),
		       COALESCE(SUM(CASE WHEN action='pull' THEN count ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN action='push' THEN count ELSE 0 END), 0),
		       MAX(day)
		FROM activity_daily
		WHERE day >= ?
		GROUP BY repository
		ORDER BY repository ASC`, since.UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RepoStat{}
	for rows.Next() {
		var s RepoStat
		var maxDay sql.NullString
		if err := rows.Scan(&s.Repository, &s.Events, &s.Pull, &s.Push, &maxDay); err != nil {
			return nil, err
		}
		if maxDay.Valid {
			v := maxDay.String
			s.LastAt = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListIgnore returns panel-managed UA ignore rules (insertion order).
func (d *Db) ListIgnore(ctx context.Context) ([]string, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT useragent FROM stats_ignore ORDER BY created_at ASC, useragent ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var ua string
		if err := rows.Scan(&ua); err != nil {
			return nil, err
		}
		out = append(out, ua)
	}
	return out, rows.Err()
}

// AddIgnore inserts one panel rule; duplicates are a no-op.
func (d *Db) AddIgnore(ctx context.Context, useragent string) error {
	_, err := d.conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO stats_ignore(useragent, created_at) VALUES (?, ?)`,
		useragent, time.Now().UTC().Format(time.RFC3339))
	return err
}

// RemoveIgnore deletes one panel rule. Missing rows are not an error.
func (d *Db) RemoveIgnore(ctx context.Context, useragent string) error {
	_, err := d.conn.ExecContext(ctx, "DELETE FROM stats_ignore WHERE useragent = ?", useragent)
	return err
}

// GetSetting returns the value for key, or "" if absent.
// Errors only on real I/O problems.
func (d *Db) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := d.conn.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSetting upserts (key, value) and bumps updated_at.
func (d *Db) SetSetting(ctx context.Context, key, value string) error {
	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, key, value, time.Now().UTC().Unix())
	return err
}

// DeleteSetting removes key. No-op if absent.
func (d *Db) DeleteSetting(ctx context.Context, key string) error {
	_, err := d.conn.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
	return err
}

func ensureParent(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}
