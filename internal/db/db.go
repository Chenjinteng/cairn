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
const SCHEMA_VERSION = 14

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
	4: `
	-- v0.5.52: persist per-UA aggregates across restarts.
	-- Source of truth for the "seen clients" panel (stats page). The
	-- in-memory h.clients map in internal/events is just a hot cache
	-- loaded at startup and flushed every 5s.
	-- Self (cairn-internal) and ignore-rule UAs are filtered upstream by
	-- events.recordClient — they never reach this table.
	CREATE TABLE IF NOT EXISTS event_seen (
		useragent     TEXT PRIMARY KEY,
		first_seen_at TEXT NOT NULL,       -- RFC3339 UTC
		last_seen_at  TEXT NOT NULL,       -- RFC3339 UTC, used by retention cleanup
		events        INTEGER NOT NULL DEFAULT 0,
		counted       INTEGER NOT NULL DEFAULT 0
	) WITHOUT ROWID;
	CREATE INDEX IF NOT EXISTS event_seen_last_seen ON event_seen(last_seen_at DESC);
	`,
	5: `
	-- v0.6.0: registry sync (cairn↔cairn) — persisted task config + run history.
	--
	-- sync_tasks: one row per configured sync. Inline bearer token (MVP);
	-- switching to credential-library references is a v0.6.2+ follow-up.
	-- include is a single TEXT column of newline-separated glob patterns
	-- (* wildcard). The engine expands each line into a regex matcher.
	--
	-- sync_runs: one row per execution. status meanings:
	--   running  — engine still iterating (engine.UpdateRun flips it on finish)
	--   success  — every repo synced, zero failures
	--   partial  — at least one repo failed but the run finished iterating
	--   failed   — run aborted before completion (remote unreachable, etc.)
	-- repos_total/synced/failed are the summary counters the UI shows.
	-- INTEGER (unix seconds) for times so the UI can compute durations cheaply.
	--
	-- FK CASCADE: deleting a task removes its run history. The engine never
	-- creates runs without a task, so no orphans are possible.
	CREATE TABLE IF NOT EXISTS sync_tasks (
		id           INTEGER PRIMARY KEY,
		name         TEXT    NOT NULL UNIQUE,
		direction    TEXT    NOT NULL CHECK(direction IN ('pull','push')),
		remote_url   TEXT    NOT NULL,
		remote_token TEXT    NOT NULL,
		include      TEXT    NOT NULL DEFAULT '',
		enabled      INTEGER NOT NULL DEFAULT 1,
		created_at   INTEGER NOT NULL,
		updated_at   INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS sync_runs (
		id            INTEGER PRIMARY KEY,
		task_id       INTEGER NOT NULL,
		started_at    INTEGER NOT NULL,
		finished_at   INTEGER,
		status        TEXT    NOT NULL CHECK(status IN ('running','success','failed','partial')),
		repos_total   INTEGER NOT NULL DEFAULT 0,
		repos_synced  INTEGER NOT NULL DEFAULT 0,
		repos_failed  INTEGER NOT NULL DEFAULT 0,
		error         TEXT    NOT NULL DEFAULT '',
		FOREIGN KEY(task_id) REFERENCES sync_tasks(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS sync_runs_task_started ON sync_runs(task_id, started_at DESC);
	`,
	6: `
	-- v0.6.3: pivot sync auth from bearer token to Basic auth. cairn's
	-- own /v2/* Basic middleware doesn't accept Bearer, so the previous
	-- "paste a bearer token" field had no source on the receiving side.
	--
	-- 0.6.1 used ALTER TABLE ... RENAME COLUMN → fails on modernc.org/sqlite
	-- v1.59.0 (parser-side "no such column" before execution). 0.6.2 used
	-- ADD COLUMN but ALSO failed on UAT because some users had a partially-
	-- built sync_tasks table from earlier broken attempts — v5's CREATE
	-- TABLE IF NOT EXISTS skips it if the table exists, but if the table
	-- was missing columns (e.g. remote_url), ADD COLUMN only adds the
	-- missing NEW columns and leaves the OLD hole in place. Then the
	-- SELECT in db/sync.go errors with "no such column: remote_url".
	--
	-- Fix: DROP TABLE + CREATE TABLE for both sync_tasks and sync_runs.
	-- Foreign keys (sync_runs.task_id → sync_tasks.id ON DELETE CASCADE)
	-- require PRAGMA foreign_keys = OFF during the rebuild so the DROP
	-- doesn't cascade-delete sync_runs before we save its data.
	--
	-- Data loss: any sync_tasks / sync_runs rows written under 0.6.0 /
	-- 0.6.1 / 0.6.2 (none of which actually worked due to auth / schema
	-- issues) are dropped. There's no working sync history to preserve
	-- across any of those releases — the engine couldn't run successfully.
	PRAGMA foreign_keys = OFF;
	DROP TABLE IF EXISTS sync_tasks;
	CREATE TABLE sync_tasks (
		id              INTEGER PRIMARY KEY,
		name            TEXT    NOT NULL UNIQUE,
		direction       TEXT    NOT NULL CHECK(direction IN ('pull','push')),
		remote_url      TEXT    NOT NULL DEFAULT '',
		remote_username TEXT    NOT NULL DEFAULT '',
		remote_password TEXT    NOT NULL DEFAULT '',
		include         TEXT    NOT NULL DEFAULT '',
		enabled         INTEGER NOT NULL DEFAULT 1,
		created_at      INTEGER NOT NULL DEFAULT 0,
		updated_at      INTEGER NOT NULL DEFAULT 0
	);
	DROP TABLE IF EXISTS sync_runs;
	CREATE TABLE sync_runs (
		id            INTEGER PRIMARY KEY,
		task_id       INTEGER NOT NULL,
		started_at    INTEGER NOT NULL,
		finished_at   INTEGER,
		status        TEXT    NOT NULL CHECK(status IN ('running','success','failed','partial')),
		repos_total   INTEGER NOT NULL DEFAULT 0,
		repos_synced  INTEGER NOT NULL DEFAULT 0,
		repos_failed  INTEGER NOT NULL DEFAULT 0,
		error         TEXT    NOT NULL DEFAULT '',
		FOREIGN KEY(task_id) REFERENCES sync_tasks(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS sync_runs_task_started ON sync_runs(task_id, started_at DESC);
	PRAGMA foreign_keys = ON;
	`,
	7: `
	-- v0.6.8: sync tasks can reference the credential library instead of
	-- carrying inline basic-auth fields (SYNC-3). remote_credential_id is
	-- credentials.Credential.ID (a STRING — hex/random, not an INTEGER),
	-- hence TEXT. Empty string means "no credential reference": the task
	-- is either anonymous (username+password both empty) or carries legacy
	-- inline credentials in remote_username / remote_password.
	--
	-- ADD COLUMN instead of the v6 DROP+CREATE rebuild: v6 already
	-- unconditionally rebuilt both sync tables, so any DB at user_version
	-- >= 6 has the exact v6 column set — there is no possible "partial
	-- table shape" left for ADD COLUMN to trip over (the v6 failure mode).
	-- Rebuilding again would additionally be riskier: inside the migration
	-- transaction PRAGMA foreign_keys = OFF is a no-op, so DROPping the
	-- parent table would CASCADE-delete sync_runs.
	ALTER TABLE sync_tasks ADD COLUMN remote_credential_id TEXT NOT NULL DEFAULT '';
	`,
	8: `
	-- v0.6.9: track which (repo, tag) the engine is currently working on, so
	-- the UI can show "正在拉 bklite/cloud-ide:v1.2.3" instead of just a
	-- counter — kills the "is it stuck?" guessing (see PR description /
	-- ROADMAP). Both columns are TEXT NOT NULL DEFAULT '' so existing rows
	-- in legacy DBs read back cleanly with empty strings (the engine is
	-- not on a specific repo at any given moment when not running).
	ALTER TABLE sync_runs ADD COLUMN current_repo TEXT NOT NULL DEFAULT '';
	ALTER TABLE sync_runs ADD COLUMN current_tag  TEXT NOT NULL DEFAULT '';
	`,
	9: `
	-- v0.6.11: per-task cron scheduling. Single ticker in the scheduler
	-- (internal/sync/scheduler.go) scans enabled schedules whose
	-- next_run_at <= now() every 30s and fires Engine.Start(task).
	-- ON DELETE CASCADE on task_id mirrors sync_runs: deleting a task
	-- tears down its schedules + runs in one transaction.
	--
	-- timezone is stored as an IANA name (e.g. "Asia/Shanghai", "" = local).
	-- Empty string is intentional: Validate() rejects non-empty values
	-- that fail time.LoadLocation, so "" is the safe default at the schema
	-- layer. next_run_at is recomputed by Validate() and after every fire,
	-- so a freshly created row always has a sane future timestamp.
	CREATE TABLE sync_schedules (
		id            INTEGER PRIMARY KEY,
		task_id       INTEGER NOT NULL,
		cron_expr     TEXT    NOT NULL DEFAULT '',
		timezone      TEXT    NOT NULL DEFAULT '',
		enabled       INTEGER NOT NULL DEFAULT 1,
		next_run_at   INTEGER NOT NULL DEFAULT 0,
		last_run_at   INTEGER,
		last_run_id   INTEGER,
		created_at    INTEGER NOT NULL DEFAULT 0,
		updated_at    INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY(task_id) REFERENCES sync_tasks(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS sync_schedules_due
		ON sync_schedules(enabled, next_run_at)
		WHERE enabled = 1;
	`,
	10: `
	-- v0.6.16: per-(repo, tag) detail rows for one sync run.
	--
	-- Before this, sync_runs only carried repos_total / repos_synced /
	-- repos_failed counters. A partial run like "76 succeeded, 1 failed
	-- on zookeeper:3.8" was visible only as a single error string with
	-- the last failing image name; the user had no way to see which other
	-- tags succeeded/failed on the same repo, or whether multiple repos
	-- failed. This table stores one row per attempted (repo, tag),
	-- symmetric to pull_jobs but inside the sync side.
	--
	-- Granularity is per (repo, tag) — matches what the engine actually
	-- iterates (one pullTag / pushTag call = one row). When a repo has
	-- many tags, this table can hold hundreds of rows per run. That's
	-- by design: the UI history modal paginates / virtual-scrolls the
	-- expanded detail, and the retention setting on sync_runs (still
	-- the UI's source-of-truth for which runs to display) can be
	-- tightened to bound growth.
	--
	-- bytes_done / bytes_total mirror pull_jobs — sync copies blobs
	-- too, so size is meaningful for both directions. state uses the
	-- same three buckets as pull_jobs (succeeded / failed / cancelled);
	-- sync currently only emits succeeded/failed because per-tag
	-- cancellation only happens when the whole run is being torn down,
	-- in which case the engine returns before writing per-tag rows.
	--
	-- ON DELETE CASCADE on run_id mirrors sync_runs(task_id): deleting
	-- the parent run (via "clear run history" or task cascade-delete)
	-- takes its items with it in one transaction.
	CREATE TABLE IF NOT EXISTS sync_run_items (
		id          INTEGER PRIMARY KEY,
		run_id      INTEGER NOT NULL,
		repository  TEXT    NOT NULL,
		tag         TEXT    NOT NULL DEFAULT '',
		state       TEXT    NOT NULL CHECK(state IN ('succeeded','failed','cancelled')),
		error       TEXT    NOT NULL DEFAULT '',
		bytes_done  INTEGER NOT NULL DEFAULT 0,
		bytes_total INTEGER NOT NULL DEFAULT 0,
		started_at  INTEGER NOT NULL,
		finished_at INTEGER,
		FOREIGN KEY(run_id) REFERENCES sync_runs(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS sync_run_items_run ON sync_run_items(run_id);
	`,
	11: `
	-- v0.7.18: persist two state categories that previously lived only in
	-- memory and disappeared on restart.
	--
	-- 1. pull_job_blobs: per-blob detail for a pull job (digest + size +
	--    status). Before this migration, j.view.Phases was in-memory only;
	--    after a container restart the history rows in pull_jobs still
	--    appeared (v0.7.12 onwards) but the UI's expanded detail ("blob
	--    #3 sha256:abcd 16.4 MB / 16.4 MB success") showed nothing for
	--    every row except the one the executor still held in memory.
	--    Operators asked to keep this — the total volume is small
	--    (50 history rows × ~20 blobs ≈ 1k rows, ~150 KB) and it's the
	--    only way to see what a past pull actually transferred.
	--
	--    WITHOUT ROWID composite primary key (pull_job_id, blob_index)
	--    matches how the writer iterates j.view.Phases in order; blob_index
	--    is the slice position at record time, not a synthetic id. ON
	--    DELETE CASCADE on pull_job_id takes the rows with the parent
	--    pull_jobs row (PullJobsEnforceLimit and PullJobDelete both rely
	--    on this).
	--
	-- 2. event_log: per-event log for the heat UI's "最近事件" panel.
	--    The in-memory ring (recentCap = 200) was the only source before;
	--    after a restart it was empty and operators couldn't see what
	--    happened yesterday. event_log keeps the most recent ~200 rows
	--    on disk and the EventLogEnforceLimit retention pass prunes
	--    anything older than that — symmetric to PullJobsEnforceLimit.
	--    Rows are inserted in event-receive order; the index on at DESC
	--    supports the "newest first" read in /api/stats/events.
	CREATE TABLE IF NOT EXISTS pull_job_blobs (
		pull_job_id TEXT    NOT NULL,
		blob_index  INTEGER NOT NULL,
		name        TEXT    NOT NULL DEFAULT '',
		digest      TEXT    NOT NULL DEFAULT '',
		size        INTEGER NOT NULL DEFAULT 0,
		status      TEXT    NOT NULL DEFAULT '',
		message     TEXT    NOT NULL DEFAULT '',
		PRIMARY KEY (pull_job_id, blob_index),
		FOREIGN KEY(pull_job_id) REFERENCES pull_jobs(id) ON DELETE CASCADE
	) WITHOUT ROWID;
	CREATE TABLE IF NOT EXISTS event_log (
		id         INTEGER PRIMARY KEY,
		at         TEXT    NOT NULL,
		event_at   TEXT    NOT NULL DEFAULT '',
		event_id   TEXT    NOT NULL DEFAULT '',
		action     TEXT    NOT NULL DEFAULT '',
		method     TEXT    NOT NULL DEFAULT '',
		media_type TEXT    NOT NULL DEFAULT '',
		repository TEXT    NOT NULL DEFAULT '',
		tag        TEXT    NOT NULL DEFAULT '',
		useragent  TEXT    NOT NULL DEFAULT '',
		addr       TEXT    NOT NULL DEFAULT '',
		host       TEXT    NOT NULL DEFAULT '',
		actor      TEXT    NOT NULL DEFAULT '',
		reason     TEXT    NOT NULL DEFAULT '',
		counted    INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS event_log_at ON event_log(at DESC);
	`,
	12: `
	-- v0.7.21: per-task "exact (repo, tag) spec" list that bypasses /v2/_catalog.
	--
	-- Background: some upstreams (e.g. TCR / Harbor behind anonymous access)
	-- return 401 / insufficient_scope on /v2/_catalog, which broke the engine
	-- on every run. UAT feedback: operators want to mirror a handful of
	-- fixed (repo, tag) pairs (e.g. bklite/alpine/openssl:3.5.4) without
	-- negotiating catalog access with the upstream — they only need the
	-- manifest for those exact refs, which docker pull does directly.
	--
	-- ADD COLUMN, not DROP+CREATE: v6 already rebuilt sync_tasks, and v7+9+10
	-- added columns incrementally, so any DB at user_version >= 6 has the
	-- exact required column set. Rebuilding again would CASCADE-delete
	-- sync_runs / sync_schedules / sync_run_items rows.
	--
	-- Format (newline-separated, parser lives in internal/sync/engine.go):
	--   library/nginx:1.27
	--   library/redis:7.4-alpine
	--   # comment lines starting with '#' are skipped (same convention as
	--   include); bad rows (no colon / empty repo or tag) are silently
	--   dropped at parse time, the same way filter.go drops bad globs.
	--
	-- Engine dispatch (runPull, v0.7.21): if TagsFilter parses to >=1 spec,
	-- skip ListRepositories + ListTags entirely and pull each (repo, tag)
	-- directly. Empty / whitespace-only TagsFilter preserves the existing
	-- catalog → include-glob path; pre-0.7.21 tasks read back with empty
	-- strings and keep their original behavior.
	ALTER TABLE sync_tasks ADD COLUMN tags_filter TEXT NOT NULL DEFAULT '';
	`,
	13: `
	-- v0.7.22: per-task "long-timeout" repo whitelist.
	--
	-- Background: sync engine's HTTP client.Timeout is 5 minutes
	-- (internal/sync/engine.go:newRemoteClient), which is right-sized for
	-- the median bklite / Docker Hub image (~50-500MB) but blows up on
	-- multi-GB mirrors like vllm:latest (23GB) or bklite/server:latest
	-- (2GB / 28 layers). At 4 MB/s public bandwidth those take 10-60
	-- minutes — single-layer body-read deadline aborts them before the
	-- last layer flushes.
	--
	-- Format (comma-separated, parsed in internal/sync/filter.go):
	--   bklite/bklite/vllm,bklite/bklite/server
	-- Empty == every repo uses the default 5min timeout (preserves the
	-- v0.7.21 behavior). Exact repo-name match (no glob) — long-timeout
	-- needs to be opt-in per repo, not a wildcard, because 30min timeout
	-- for hundreds of small repos would mask fast failures (e.g. 404 on
	-- a typo'd spec).
	--
	-- Engine dispatch (runPull, v0.7.22): each spec is matched against
	-- this list; matching repos get a 30min client timeout, others keep
	-- the 5min default. ADD COLUMN not DROP+CREATE — same discipline as
	-- v12; pre-0.7.22 tasks read back with empty string and never match.
	ALTER TABLE sync_tasks ADD COLUMN long_timeout_repos TEXT NOT NULL DEFAULT '';
	`,
	14: `
	-- v0.7.25: per-spec timeout decision surfaced to the UI.
	--
	-- Background: v0.7.24 made pullTag auto-pick between DefaultSyncTimeout
	-- (5 min, for normal small/medium images) and LongSyncTimeout (30 min,
	-- for big mirrors whose manifest total > 1 GiB). The decision is
	-- correct, but completely invisible — operators staring at a
	-- "running" run row have no way to tell whether the engine is on
	-- the fast 5-min clock or the slow 30-min clock, and start to
	-- wonder "is this hung?" after the first 6 minutes of a big-image
	-- pull.
	--
	-- Resolution: stamp every sync_run_items row with which timeout was
	-- used ('default' vs 'long'). UI renders the chip next to the
	-- duration column — small mirrors stay quiet (default = no chip,
	-- the 5min clock is the "normal" expectation), big mirrors show an
	-- orange "30min" tag so the operator knows the engine is on the long
	-- deadline.
	--
	-- ADD COLUMN not DROP+CREATE — same discipline as v0.7.21/22.
	-- Empty string default preserves the v0.7.21-v0.7.24 contract for
	-- pre-existing rows (timeout decision made but unrecorded); UI
	-- treats empty == default (5min, no chip).
	ALTER TABLE sync_run_items ADD COLUMN timeout_used TEXT NOT NULL DEFAULT '';
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

// PullJobCancel marks a row as cancelled, but only if it is still in a
// pre-terminal state ("running" or "queued"). Returns (true, nil) when
// the row was updated, (false, nil) when the row didn't exist or was
// already terminal. v0.7.17: lets the Cancel API recover from ghost
// "running" rows that older code paths left in pull_jobs before the
// state flip — those rows were no longer in the executor's e.jobs map,
// so calling Executor.Cancel(id) returned ErrJobNotFound. This method
// is the fallback for exactly that case.
//
// A non-existent row is indistinguishable from "already terminal" to
// the caller; both are no-ops (the UI will simply not find the row to
// cancel). When the row IS flipped, ended_at is set to now so the
// history list renders the cancellation time honestly.
func (d *Db) PullJobCancel(ctx context.Context, id string) (bool, error) {
	res, err := d.conn.ExecContext(ctx, `
		UPDATE pull_jobs
		   SET state = 'cancelled',
		       ended_at = ?
		 WHERE id = ?
		   AND state IN ('running', 'queued')
	`, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return false, fmt.Errorf("db: pull_jobs cancel: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("db: pull_jobs cancel rows: %w", err)
	}
	return n > 0, nil
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

// PullJobDelete removes one history row by ID. Returns true if a row
// was actually deleted, false if the ID didn't exist.
//
// v0.7.15: the API used to fail with "pull job not found" whenever the
// caller tried to remove a SQLite-only job (i.e. one the executor had
// forgotten after a restart). DeletePullJob in handlers_extra now
// falls through to this when Executor.Delete returns ErrJobNotFound.
func (d *Db) PullJobDelete(ctx context.Context, id string) (bool, error) {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM pull_jobs WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("db: pull_jobs delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("db: pull_jobs rows affected: %w", err)
	}
	return n > 0, nil
}

// PullJobsEnforceLimit caps pull_jobs to `limit` rows, keeping the newest
// ones (by started_at DESC). Excess rows are deleted.
//
// v0.7.16: pull_jobs had no retention — every terminal job accumulated
// forever and the table grew unbounded. The retentionLoop in server.go
// calls this once per pass with limit=50, matching the product call of
// "keep 50 history rows".
//
// The query keeps `limit` rows then deletes everything older by id
// (id encodes the timestamp prefix in our generator, so the inverse
// order matches started_at — no extra sort needed).
//
// limit <= 0 is a no-op (returns 0, nil) so callers don't have to guard.
func (d *Db) PullJobsEnforceLimit(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	res, err := d.conn.ExecContext(ctx, `
		DELETE FROM pull_jobs
		WHERE id NOT IN (
		    SELECT id FROM pull_jobs
		    ORDER BY started_at DESC
		    LIMIT ?
		)
	`, limit)
	if err != nil {
		return 0, fmt.Errorf("db: pull_jobs enforce limit: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("db: pull_jobs enforce limit rows: %w", err)
	}
	return n, nil
}

// PullJobsList returns every row in pull_jobs, newest first by started_at.
// Used by ListPullJobs at API time to fold SQLite history into the live
// memory view — without this, a container restart would surface as an
// empty history list because Executor.List() only walks memory.
//
// (v0.7.12: the dispatcher for "restart loses history".)
//
// Memory jobs that the executor still has are passed in so we can skip
// rows whose ID is already in memory — otherwise a job that just finished
// would show up twice in the UI (once from memory, once from SQLite).
// Pure memory reads don't filter; the caller decides.
func (d *Db) PullJobsList(ctx context.Context, limit int) ([]PullJobRow, error) {
	q := `SELECT id, source_ref, dest_repo, dest_tag, state, error,
	             bytes_done, bytes_total, started_at, ended_at, created_at
	      FROM pull_jobs
	      ORDER BY started_at DESC`
	if limit > 0 {
		q += ` LIMIT ?`
	}
	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = d.conn.QueryContext(ctx, q, limit)
	} else {
		rows, err = d.conn.QueryContext(ctx, q)
	}
	if err != nil {
		return nil, fmt.Errorf("db: pull_jobs list: %w", err)
	}
	defer rows.Close()
	out := make([]PullJobRow, 0)
	for rows.Next() {
		var (
			r            PullJobRow
			startedAtStr string
			endedAtStr   string
			createdAtStr string
		)
		if err := rows.Scan(&r.ID, &r.SourceRef, &r.DestRepo, &r.DestTag, &r.State, &r.Error,
			&r.BytesDone, &r.BytesTotal, &startedAtStr, &endedAtStr, &createdAtStr); err != nil {
			return nil, fmt.Errorf("db: pull_jobs scan: %w", err)
		}
		r.StartedAt = parseTimeOrZero(startedAtStr)
		r.EndedAt = parseTimeOrZero(endedAtStr)
		r.CreatedAt = parseTimeOrZero(createdAtStr)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: pull_jobs iterate: %w", err)
	}
	return out, nil
}

// parseTimeOrZero accepts the RFC3339 string we write and returns the
// time.Time, or zero on a parse error so the API can still serve the
// row instead of 500-ing on one malformed timestamp.
func parseTimeOrZero(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parseTimeOrZeroOrNil accepts the RFC3339Nano string used in event_log
// and returns (time.Time, nil) on success or (zero, parse-error) on
// failure. Returning the error lets callers decide whether an unparsable
// timestamp should skip the row or zero the field silently — EventLogList
// chooses the latter so a single corrupt row doesn't kill the whole list.
func parseTimeOrZeroOrNil(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
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

// RetentionCleanup deletes activity + seen-client aggregates older than
// cutoff (days). Idempotent. Both tables share the same retention
// horizon (cfg.StatsRetentionDays()) so the "seen clients" panel never
// outlives the heat data it was derived from. Returns total rows removed
// across both tables.
func (d *Db) RetentionCleanup(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := d.conn.ExecContext(ctx, `
		DELETE FROM activity_daily WHERE day < ?
	`, cutoff.UTC().Format("2006-01-02"))
	if err != nil {
		return 0, err
	}
	n1, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	res2, err := d.conn.ExecContext(ctx, `
		DELETE FROM event_seen WHERE last_seen_at < ?
	`, cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return n1, err
	}
	n2, err := res2.RowsAffected()
	if err != nil {
		return n1, err
	}
	return n1 + n2, nil
}

// PurgeAll deletes every heat row + every seen-client row (settings
// page "clear heat data"). Idempotent. Returns separate counts so the
// UI can report what was cleared (activity rows vs seen-client rows
// aren't the same kind of data and operators want both numbers).
func (d *Db) PurgeAll(ctx context.Context) (activity, seen int64, err error) {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM activity_daily`)
	if err != nil {
		return 0, 0, err
	}
	activity, err = res.RowsAffected()
	if err != nil {
		return 0, 0, err
	}

	res2, err := d.conn.ExecContext(ctx, `DELETE FROM event_seen`)
	if err != nil {
		return activity, 0, err
	}
	seen, err = res2.RowsAffected()
	if err != nil {
		return activity, seen, err
	}
	return activity, seen, nil
}

// EventSeenRow is one row from event_seen (per-UA aggregate). It mirrors
// internal/events.clientAgg's persisted shape.
type EventSeenRow struct {
	UserAgent   string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	Events      int64
	Counted     int64
}

// LoadAllEventSeen returns every persisted client aggregate. Used by
// events.NewHandler at startup to populate the in-memory hot cache so
// the "seen clients" panel survives restarts.
func (d *Db) LoadAllEventSeen(ctx context.Context) ([]EventSeenRow, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT useragent, first_seen_at, last_seen_at, events, counted
		FROM event_seen
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EventSeenRow
	for rows.Next() {
		var r EventSeenRow
		var first, last string
		if err := rows.Scan(&r.UserAgent, &first, &last, &r.Events, &r.Counted); err != nil {
			return nil, err
		}
		r.FirstSeenAt, _ = time.Parse(time.RFC3339, first)
		r.LastSeenAt, _ = time.Parse(time.RFC3339, last)
		out = append(out, r)
	}
	return out, rows.Err()
}

// BatchUpsertEventSeen writes (or replaces) multiple event_seen rows in
// one transaction. events / counted are absolute values tracked by the
// caller from startup load onward; SQL uses ON CONFLICT to overwrite,
// not accumulate — keeps the math simple and avoids the "flush window
// double-count" trap.
//
// Self and ignore-rule UAs are filtered by events.recordClient upstream
// and never reach here.
func (d *Db) BatchUpsertEventSeen(ctx context.Context, rows []EventSeenRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var sb strings.Builder
	sb.WriteString(`INSERT INTO event_seen (useragent, first_seen_at, last_seen_at, events, counted) VALUES `)
	args := make([]any, 0, len(rows)*5)
	for i, r := range rows {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(?, ?, ?, ?, ?)")
		args = append(args,
			r.UserAgent,
			r.FirstSeenAt.UTC().Format(time.RFC3339),
			r.LastSeenAt.UTC().Format(time.RFC3339),
			r.Events,
			r.Counted,
		)
	}
	sb.WriteString(` ON CONFLICT(useragent) DO UPDATE SET
		first_seen_at = excluded.first_seen_at,
		last_seen_at  = excluded.last_seen_at,
		events        = excluded.events,
		counted       = excluded.counted`)

	if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
		return err
	}
	return tx.Commit()
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

// PullJobBlobRow mirrors pull.Phase for the blob / config / child-manifests
// steps that the pull executor records once a job reaches a terminal state.
// `Index` is the position in j.view.Phases at the moment of recording;
// matches the order the UI shows when the row is expanded.
type PullJobBlobRow struct {
	JobID   string
	Index   int
	Name    string
	Digest  string
	Size    int64
	Status  string
	Message string
}

// PullJobBlobsRecord persists a batch of blob rows for one job, replacing
// any rows that already exist for (job_id, index). Idempotent: calling
// twice for the same job keeps the second batch (the executor only calls
// this once per terminal job, but the upsert shape lets the operator's
// "rebuild history" future feature be safe to re-run).
//
// v0.7.18: this is the on-disk counterpart of j.view.Phases. Before this
// migration, blob detail lived only in memory and disappeared when the
// executor forgot the job — every past pull in the history list showed
// an empty expanded row.
func (d *Db) PullJobBlobsRecord(ctx context.Context, rows []PullJobBlobRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO pull_job_blobs
		    (pull_job_id, blob_index, name, digest, size, status, message)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pull_job_id, blob_index) DO UPDATE SET
		    name = excluded.name,
		    digest = excluded.digest,
		    size = excluded.size,
		    status = excluded.status,
		    message = excluded.message
	`)
	if err != nil {
		return fmt.Errorf("db: pull_job_blobs prepare: %w", err)
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx,
			r.JobID, r.Index, r.Name, r.Digest, r.Size, r.Status, r.Message,
		); err != nil {
			return fmt.Errorf("db: pull_job_blobs insert: %w", err)
		}
	}
	return tx.Commit()
}

// PullJobBlobsList returns the blob rows for one job in slice order
// (blob_index ASC). Returns nil + nil if the job has no recorded blobs
// (e.g. a pre-v0.7.18 history row).
func (d *Db) PullJobBlobsList(ctx context.Context, jobID string) ([]PullJobBlobRow, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT pull_job_id, blob_index, name, digest, size, status, message
		  FROM pull_job_blobs
		 WHERE pull_job_id = ?
		 ORDER BY blob_index ASC
	`, jobID)
	if err != nil {
		return nil, fmt.Errorf("db: pull_job_blobs list: %w", err)
	}
	defer rows.Close()
	var out []PullJobBlobRow
	for rows.Next() {
		var r PullJobBlobRow
		if err := rows.Scan(&r.JobID, &r.Index, &r.Name, &r.Digest, &r.Size, &r.Status, &r.Message); err != nil {
			return nil, fmt.Errorf("db: pull_job_blobs scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: pull_job_blobs iterate: %w", err)
	}
	return out, nil
}

// EventLogRow is one entry in the heat UI's "最近事件" panel.
//
// v0.7.18: identical JSON shape to events.RecentEvent (one fewer hop when
// the API layer is just proxying to JSON). The mapping is mechanical —
// see internal/api/handlers_stats.go where RecentEvents reads from this
// table to fall back to disk when the in-memory ring is empty.
type EventLogRow struct {
	At        time.Time
	EventAt   time.Time
	EventID   string
	Action    string
	Method    string
	MediaType string
	Repository string
	Tag       string
	UserAgent string
	Addr      string
	Host      string
	Actor     string
	Reason    string
	Counted   bool
}

// EventLogRecord writes one row to event_log. The caller supplies
// EventAt / At as RFC3339; the receiver does no time parsing.
func (d *Db) EventLogRecord(ctx context.Context, r EventLogRow) error {
	counted := 0
	if r.Counted {
		counted = 1
	}
	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO event_log
		    (at, event_at, event_id, action, method, media_type,
		     repository, tag, useragent, addr, host, actor, reason, counted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.At.UTC().Format(time.RFC3339Nano),
		r.EventAt.UTC().Format(time.RFC3339Nano),
		r.EventID, r.Action, r.Method, r.MediaType,
		r.Repository, r.Tag, r.UserAgent, r.Addr, r.Host,
		r.Actor, r.Reason, counted)
	if err != nil {
		return fmt.Errorf("db: event_log insert: %w", err)
	}
	return nil
}

// EventLogList returns up to `limit` rows from event_log in at-DESC
// order. limit <= 0 returns all rows (use with care on long-lived DBs).
func (d *Db) EventLogList(ctx context.Context, limit int) ([]EventLogRow, error) {
	q := `SELECT at, event_at, event_id, action, method, media_type,
	             repository, tag, useragent, addr, host, actor, reason, counted
	        FROM event_log
	       ORDER BY at DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: event_log list: %w", err)
	}
	defer rows.Close()
	var out []EventLogRow
	for rows.Next() {
		var (
			r       EventLogRow
			at, eat string
			counted int
		)
		if err := rows.Scan(&at, &eat, &r.EventID, &r.Action, &r.Method,
			&r.MediaType, &r.Repository, &r.Tag, &r.UserAgent, &r.Addr,
			&r.Host, &r.Actor, &r.Reason, &counted); err != nil {
			return nil, fmt.Errorf("db: event_log scan: %w", err)
		}
		if t, perr := parseTimeOrZeroOrNil(at); perr == nil {
			r.At = t
		}
		if t, perr := parseTimeOrZeroOrNil(eat); perr == nil {
			r.EventAt = t
		}
		r.Counted = counted != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: event_log iterate: %w", err)
	}
	return out, nil
}

// EventLogEnforceLimit caps event_log to `limit` rows, keeping the newest
// ones (by at DESC). Mirrors PullJobsEnforceLimit: same single-statement
// pattern, same atomic DELETE.
//
// v0.7.18: the in-memory ring (recentCap = 200) had no on-disk
// counterpart, so a restart wiped "最近事件" clean. 200 is the same
// number the ring uses, matching what the UI was already rendering
// before the restart.
//
// limit <= 0 is a no-op.
func (d *Db) EventLogEnforceLimit(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	res, err := d.conn.ExecContext(ctx, `
		DELETE FROM event_log
		WHERE id NOT IN (
		    SELECT id FROM event_log
		    ORDER BY at DESC
		    LIMIT ?
		)
	`, limit)
	if err != nil {
		return 0, fmt.Errorf("db: event_log enforce limit: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("db: event_log enforce limit rows: %w", err)
	}
	return n, nil
}

// EventLogCounts groups the totals /api/stats/events returns. accepted
// is COUNT WHERE counted=1 (events that hit heat); rejected is COUNT
// WHERE counted=0 (events that didn't, e.g. failed signature). self /
// ignored were tracked as atomic counters pre-v0.7.20; resurrecting
// them needs an event_log schema bump, so for now they're always 0 in
// the API output — the StatsEvents handler stamps them as zero so the
// UI shape stays stable.
type EventLogCounts struct {
	Total    int64 // COUNT(*)
	Accepted int64 // COUNT(*) WHERE counted = 1
	Rejected int64 // COUNT(*) WHERE counted = 0
}

// EventLogCount returns one COUNT(*) over event_log, plus the
// counted=1 / counted=0 split. Single transaction so the totals agree
// at the same instant (the two queries see the same row set).
//
// v0.7.20: replaces the in-memory atomic counters on Handler; SQLite
// is fast enough that an aggregation query per /api/stats/events call
// is cheaper than maintaining dual state and worrying about restart
// inconsistency.
func (d *Db) EventLogCount(ctx context.Context) (EventLogCounts, error) {
	var c EventLogCounts
	if err := d.conn.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(counted), 0) FROM event_log`,
	).Scan(&c.Total, &c.Accepted); err != nil {
		return c, fmt.Errorf("db: event_log count: %w", err)
	}
	c.Rejected = c.Total - c.Accepted
	return c, nil
}

func ensureParent(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}
