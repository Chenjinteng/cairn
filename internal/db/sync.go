package db

import (
	"context"
	"database/sql"
	"time"
)

// v0.6.1 sync DB methods. Row types live here (not in internal/sync)
// to avoid the "internal/db imports its consumer" anti-pattern; the
// domain types in internal/sync/types.go are constructed by
// internal/sync/store.go via the rowToTask / rowToRun converters.
//
// v0.6.1: schema is now v6 (see migrations in db.go). The auth columns
// pivoted from `remote_token TEXT` (single bearer token) to
// `remote_username TEXT` + `remote_password TEXT` (basic-auth pair, matching
// cairn's own registry middleware). The migration renames the old column
// and adds the new one with DEFAULT ''.

// SyncTaskRow is the SQL-side view of one sync_tasks row. Times are
// stored as INTEGER unix seconds and decoded on read.
//
// RemoteCredentialID references a credentials-library entry (v0.6.8 /
// SYNC-3, schema v7). Empty means the task is either anonymous or uses
// the legacy inline RemoteUsername / RemotePassword pair.
//
// LastRunStatus is NOT a sync_tasks column — SyncTaskGet / SyncTaskList
// populate it from a subquery over sync_runs so the UI can disable the
// "run" button while a background run is in flight (SYNC-4). Empty when
// the task has no runs yet.
type SyncTaskRow struct {
	ID                 int64
	Name               string
	Direction          string // 'pull' | 'push' — string at this layer, enum upstream
	RemoteURL          string
	RemoteUsername     string
	RemotePassword     string
	RemoteCredentialID string
	Include            string
	TagsFilter         string // v0.7.21: per-task (repo, tag) spec list; empty = use catalog path
	LongTimeoutRepos   string // v0.7.22: comma-separated repo names that get 30min client timeout; "" = all repos use 5min
	Enabled            bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// LastRunStatus / LastRunID / LastRunCurrentRepo / LastRunCurrentTag
	// are NOT sync_tasks columns. SyncTaskGet / SyncTaskList fill them from
	// the newest sync_runs row via a correlated subquery. The engine writes
	// current_repo / current_tag on every iteration step (v0.6.9), so the
	// UI can show "正在拉 repo:tag" while a run is in flight.
	// All empty when the task has never run.
	LastRunStatus      string
	LastRunID          int64
	LastRunCurrentRepo string
	LastRunCurrentTag  string
}

// SyncRunRow is the SQL-side view of one sync_runs row.
type SyncRunRow struct {
	ID          int64
	TaskID      int64
	StartedAt   time.Time
	FinishedAt  *time.Time // nil while running
	Status      string     // 'running' | 'success' | 'partial' | 'failed'
	ReposTotal  int
	ReposSynced int
	ReposFailed int
	Error       string
	// CurrentRepo / CurrentTag capture which (repo, tag) the engine is
	// currently iterating (v0.6.9). Empty while not running. Stays set
	// after the run finishes — useful when the run failed mid-iteration,
	// so the UI can point at "stuck at this image" without log-diving.
	CurrentRepo string
	CurrentTag  string
}

// SyncScheduleRow is the SQL-side view of one sync_schedules row
// (v0.6.11). Times are Unix seconds (UTC); bools are 0/1. The DB never
// validates the cron expression or timezone — that's domain-level
// (sync.Schedule.Validate). NextRunAt is populated by Validate() and
// after every fire, so a freshly-created row always has a sane future
// timestamp.
type SyncScheduleRow struct {
	ID         int64
	TaskID     int64
	CronExpr   string
	Timezone   string // "" = UTC
	Enabled    bool
	NextRunAt  time.Time
	LastRunAt  *time.Time
	LastRunID  *int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SyncTaskCreate inserts a new task and returns the assigned ID. UNIQUE
// constraint violations (name conflict) bubble up as raw SQLite errors;
// the handler layer matches on the error message and translates to 409.
func (d *Db) SyncTaskCreate(ctx context.Context, t SyncTaskRow) (int64, error) {
	enabled := 0
	if t.Enabled {
		enabled = 1
	}
	res, err := d.conn.ExecContext(ctx, `
		INSERT INTO sync_tasks(name, direction, remote_url, remote_username, remote_password, remote_credential_id, include, tags_filter, long_timeout_repos, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, t.Name, t.Direction, t.RemoteURL, t.RemoteUsername, t.RemotePassword, t.RemoteCredentialID, t.Include, t.TagsFilter, t.LongTimeoutRepos, enabled,
		t.CreatedAt.Unix(), t.UpdatedAt.Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SyncTaskGet returns one row by ID. Returns sql.ErrNoRows when not found.
// LastRunStatus is filled from the newest sync_runs row (empty string when none).
func (d *Db) SyncTaskGet(ctx context.Context, id int64) (SyncTaskRow, error) {
	var t SyncTaskRow
	var enabled int
	var createdAt, updatedAt int64
	err := d.conn.QueryRowContext(ctx, `
		SELECT id, name, direction, remote_url, remote_username, remote_password, remote_credential_id, include, tags_filter, long_timeout_repos, enabled, created_at, updated_at,
		       COALESCE((SELECT r.status       FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), ''),
		       COALESCE((SELECT r.id           FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), 0),
		       COALESCE((SELECT r.current_repo FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), ''),
		       COALESCE((SELECT r.current_tag  FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), '')
		FROM sync_tasks WHERE id = ?
	`, id).Scan(&t.ID, &t.Name, &t.Direction, &t.RemoteURL, &t.RemoteUsername, &t.RemotePassword, &t.RemoteCredentialID, &t.Include, &t.TagsFilter, &t.LongTimeoutRepos, &enabled,
		&createdAt, &updatedAt, &t.LastRunStatus, &t.LastRunID, &t.LastRunCurrentRepo, &t.LastRunCurrentTag)
	if err != nil {
		return SyncTaskRow{}, err
	}
	t.Enabled = enabled != 0
	t.CreatedAt = time.Unix(createdAt, 0).UTC()
	t.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return t, nil
}

// SyncTaskList returns every task, newest first (created_at DESC, id DESC
// as tiebreaker — same-second inserts sort by insertion order).
func (d *Db) SyncTaskList(ctx context.Context) ([]SyncTaskRow, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT id, name, direction, remote_url, remote_username, remote_password, remote_credential_id, include, tags_filter, long_timeout_repos, enabled, created_at, updated_at,
		       COALESCE((SELECT r.status       FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), ''),
		       COALESCE((SELECT r.id           FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), 0),
		       COALESCE((SELECT r.current_repo FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), ''),
		       COALESCE((SELECT r.current_tag  FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), '')
		FROM sync_tasks ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncTaskRow
	for rows.Next() {
		var t SyncTaskRow
		var enabled int
		var createdAt, updatedAt int64
		if err := rows.Scan(&t.ID, &t.Name, &t.Direction, &t.RemoteURL, &t.RemoteUsername, &t.RemotePassword, &t.RemoteCredentialID, &t.Include, &t.TagsFilter, &t.LongTimeoutRepos, &enabled,
			&createdAt, &updatedAt, &t.LastRunStatus, &t.LastRunID, &t.LastRunCurrentRepo, &t.LastRunCurrentTag); err != nil {
			return nil, err
		}
		t.Enabled = enabled != 0
		t.CreatedAt = time.Unix(createdAt, 0).UTC()
		t.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

// SyncTaskUpdate replaces name/direction/url/credentials/credential-ref/
// include/enabled for the given ID. created_at is preserved; updated_at
// is overwritten.
func (d *Db) SyncTaskUpdate(ctx context.Context, t SyncTaskRow) error {
	enabled := 0
	if t.Enabled {
		enabled = 1
	}
	res, err := d.conn.ExecContext(ctx, `
		UPDATE sync_tasks SET name=?, direction=?, remote_url=?, remote_username=?, remote_password=?, remote_credential_id=?, include=?, tags_filter=?, long_timeout_repos=?, enabled=?, updated_at=?
		WHERE id=?
	`, t.Name, t.Direction, t.RemoteURL, t.RemoteUsername, t.RemotePassword, t.RemoteCredentialID, t.Include, t.TagsFilter, t.LongTimeoutRepos, enabled,
		t.UpdatedAt.Unix(), t.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SyncTaskDelete removes a task. The CASCADE foreign key removes its
// runs atomically inside the same transaction that deletes the task
// (FK enforcement is enabled — see Open's _pragma=foreign_keys(1)).
func (d *Db) SyncTaskDelete(ctx context.Context, id int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM sync_tasks WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SyncRunCreate inserts a new run row when the engine starts iterating.
// Returns the assigned ID. The engine will call SyncRunUpdate when the
// run finishes or aborts.
func (d *Db) SyncRunCreate(ctx context.Context, r SyncRunRow) (int64, error) {
	var finishedAt *int64
	if r.FinishedAt != nil {
		v := r.FinishedAt.Unix()
		finishedAt = &v
	}
	res, err := d.conn.ExecContext(ctx, `
		INSERT INTO sync_runs(task_id, started_at, finished_at, status, repos_total, repos_synced, repos_failed, error, current_repo, current_tag)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.TaskID, r.StartedAt.Unix(), finishedAt, r.Status,
		r.ReposTotal, r.ReposSynced, r.ReposFailed, r.Error, r.CurrentRepo, r.CurrentTag)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SyncRunTrimOlder deletes runs older than the most recent `keep` runs
// for a task. Called after each new run (SyncRunCreate) so the table
// stays bounded — v0.6.30 introduces this; before that, runs accumulated
// forever (UI showed only the latest 50, but DB grew unboundedly).
//
// `keep` is the number of newest runs to retain per task; older ones are
// removed. ON DELETE CASCADE on sync_run_items(run_id) (db.go v6+) takes
// the per-tag detail rows with the parent runs in one statement.
//
// Returns the number of runs actually deleted — useful for logging, but
// callers can ignore it (the trim is best-effort; failure here shouldn't
// abort the new run that triggered it).
func (d *Db) SyncRunTrimOlder(ctx context.Context, taskID int64, keep int) (int64, error) {
	if keep <= 0 {
		// Defensive: caller passed 0 or negative. Treat as "no trim" — the
		// DB-keep-default falls back to the UI's historical limit (50),
		// not infinity. We never want this path to mean "delete all".
		return 0, nil
	}
	res, err := d.conn.ExecContext(ctx, `
		DELETE FROM sync_runs
		WHERE task_id = ?
		  AND id NOT IN (
		    SELECT id FROM sync_runs
		    WHERE task_id = ?
		    ORDER BY started_at DESC, id DESC
		    LIMIT ?
		  )
	`, taskID, taskID, keep)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// SyncRunUpdate is called once when the engine finishes a run (success,
// partial, or failed). Replaces finished_at, status, summary counters,
// and the error string. task_id is immutable.
//
// current_repo / current_tag are intentionally NOT cleared here — keeping
// the last position lets the UI highlight "failed at this image" for a
// failed run (v0.6.9). Empty on a brand-new run.
func (d *Db) SyncRunUpdate(ctx context.Context, r SyncRunRow) error {
	var finishedAt *int64
	if r.FinishedAt != nil {
		v := r.FinishedAt.Unix()
		finishedAt = &v
	}
	res, err := d.conn.ExecContext(ctx, `
		UPDATE sync_runs SET finished_at=?, status=?, repos_total=?, repos_synced=?, repos_failed=?, error=?
		WHERE id=?
	`, finishedAt, r.Status, r.ReposTotal, r.ReposSynced, r.ReposFailed, r.Error, r.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SyncRunUpdateProgress writes ONLY current_repo and current_tag for the
// given run id. Used by the engine on every (repo, tag) transition during
// iteration (v0.6.9). Keeping this update narrow — counters / status /
// finished_at are untouched — means concurrent reads from the UI poll
// never see a partially-flushed snapshot of a half-baked run.
//
// Single-row UPDATE; cheap enough to fire hundreds of times per run.
func (d *Db) SyncRunUpdateProgress(ctx context.Context, id int64, currentRepo, currentTag string) error {
	res, err := d.conn.ExecContext(ctx, `
		UPDATE sync_runs SET current_repo=?, current_tag=?
		WHERE id=?
	`, currentRepo, currentTag, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SyncRunListByTask returns runs for a task, newest first. limit<=0
// means no limit; the UI passes 10 (recent runs only — older runs are
// trimmed by SyncRunTrimOlder after each new run, so 10 covers everything
// still in DB).
func (d *Db) SyncRunListByTask(ctx context.Context, taskID int64, limit int) ([]SyncRunRow, error) {
	query := `
		SELECT id, task_id, started_at, finished_at, status, repos_total, repos_synced, repos_failed, error, current_repo, current_tag
		FROM sync_runs WHERE task_id = ? ORDER BY started_at DESC, id DESC
	`
	var (
		rows *sql.Rows
		err  error
	)
	if limit > 0 {
		rows, err = d.conn.QueryContext(ctx, query+` LIMIT ?`, taskID, limit)
	} else {
		rows, err = d.conn.QueryContext(ctx, query, taskID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncRunRow
	for rows.Next() {
		var r SyncRunRow
		var startedAt int64
		var finishedAt sql.NullInt64
		if err := rows.Scan(&r.ID, &r.TaskID, &startedAt, &finishedAt, &r.Status,
			&r.ReposTotal, &r.ReposSynced, &r.ReposFailed, &r.Error,
			&r.CurrentRepo, &r.CurrentTag); err != nil {
			return nil, err
		}
		r.StartedAt = time.Unix(startedAt, 0).UTC()
		if finishedAt.Valid {
			t := time.Unix(finishedAt.Int64, 0).UTC()
			r.FinishedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SyncRunMarkRunningFailed flips every leftover 'running' run row to
// 'failed' and stamps finished_at. Called once at process startup.
//
// Runs execute in process memory, so during startup no run can genuinely
// be running: every 'running' row is a zombie whose process exited
// (crash / docker restart) before SyncRunUpdate could fire. Without this
// sweep the UI would render a permanently stuck "running" state and, as
// of v0.6.8, refuse to start new runs for that task (SYNC-1 / SYNC-4).
//
// Returns the number of rows fixed (0 on a healthy start).
func (d *Db) SyncRunMarkRunningFailed(ctx context.Context, finishedAt time.Time, reason string) (int64, error) {
	res, err := d.conn.ExecContext(ctx, `
		UPDATE sync_runs SET status='failed', finished_at=?, error=?
		WHERE status='running'
	`, finishedAt.Unix(), reason)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SyncScheduleCreate inserts one schedule. Caller is responsible for
// validating CronExpr / Timezone and populating NextRunAt. Returns
// the assigned id; sets r.ID in place would be a future nicety but
// store.ScheduleCreate handles ID assignment instead.
func (d *Db) SyncScheduleCreate(ctx context.Context, r SyncScheduleRow) (int64, error) {
	enabled := 0
	if r.Enabled {
		enabled = 1
	}
	var lastRunAt *int64
	if r.LastRunAt != nil {
		v := r.LastRunAt.Unix()
		lastRunAt = &v
	}
	res, err := d.conn.ExecContext(ctx, `
		INSERT INTO sync_schedules(task_id, cron_expr, timezone, enabled, next_run_at, last_run_at, last_run_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.TaskID, r.CronExpr, r.Timezone, enabled, r.NextRunAt.Unix(), lastRunAt, r.LastRunID, r.CreatedAt.Unix(), r.UpdatedAt.Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SyncScheduleUpdate replaces cron_expr / timezone / enabled / next_run_at
// / last_run_at / last_run_id on the row identified by id. updated_at
// is overwritten to time.Now().UTC() by the caller.
func (d *Db) SyncScheduleUpdate(ctx context.Context, r SyncScheduleRow) error {
	enabled := 0
	if r.Enabled {
		enabled = 1
	}
	var lastRunAt *int64
	if r.LastRunAt != nil {
		v := r.LastRunAt.Unix()
		lastRunAt = &v
	}
	res, err := d.conn.ExecContext(ctx, `
		UPDATE sync_schedules SET cron_expr=?, timezone=?, enabled=?, next_run_at=?, last_run_at=?, last_run_id=?, updated_at=?
		WHERE id=?
	`, r.CronExpr, r.Timezone, enabled, r.NextRunAt.Unix(), lastRunAt, r.LastRunID, r.UpdatedAt.Unix(), r.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SyncScheduleDelete removes a schedule. Returns sql.ErrNoRows when
// id doesn't exist (the store layer translates this to ErrScheduleNotFound).
func (d *Db) SyncScheduleDelete(ctx context.Context, id int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM sync_schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SyncScheduleListByTask returns schedules for a task, ordered by id
// (effectively creation order). limit <= 0 means no limit. The UI
// passes 50 by convention; schedules per task should stay small.
func (d *Db) SyncScheduleListByTask(ctx context.Context, taskID int64, limit int) ([]SyncScheduleRow, error) {
	query := `
		SELECT id, task_id, cron_expr, timezone, enabled, next_run_at, last_run_at, last_run_id, created_at, updated_at
		FROM sync_schedules WHERE task_id = ? ORDER BY id ASC
	`
	var (
		rows *sql.Rows
		err  error
	)
	if limit > 0 {
		rows, err = d.conn.QueryContext(ctx, query+` LIMIT ?`, taskID, limit)
	} else {
		rows, err = d.conn.QueryContext(ctx, query, taskID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncScheduleRow
	for rows.Next() {
		var r SyncScheduleRow
		var enabled int
		var nextRunAt int64
		var lastRunAt, lastRunID sql.NullInt64
		var createdAt, updatedAt int64
		if err := rows.Scan(&r.ID, &r.TaskID, &r.CronExpr, &r.Timezone, &enabled, &nextRunAt, &lastRunAt, &lastRunID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		r.NextRunAt = time.Unix(nextRunAt, 0).UTC()
		if lastRunAt.Valid {
			t := time.Unix(lastRunAt.Int64, 0).UTC()
			r.LastRunAt = &t
		}
		if lastRunID.Valid {
			id := lastRunID.Int64
			r.LastRunID = &id
		}
		r.CreatedAt = time.Unix(createdAt, 0).UTC()
		r.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// SyncScheduleListDue returns every enabled schedule whose next_run_at
// <= now. The scheduler loop calls this every 30s and processes the
// returned batch. Ordering is by next_run_at ASC so the most overdue
// schedule fires first (mostly moot since each schedule has its own
// task; ordering matters only when many schedules hit the same wall
// clock minute).
func (d *Db) SyncScheduleListDue(ctx context.Context, now time.Time, limit int) ([]SyncScheduleRow, error) {
	query := `
		SELECT id, task_id, cron_expr, timezone, enabled, next_run_at, last_run_at, last_run_id, created_at, updated_at
		FROM sync_schedules WHERE enabled = 1 AND next_run_at <= ?
		ORDER BY next_run_at ASC
	`
	var (
		rows *sql.Rows
		err  error
	)
	if limit > 0 {
		rows, err = d.conn.QueryContext(ctx, query+` LIMIT ?`, now.Unix(), limit)
	} else {
		rows, err = d.conn.QueryContext(ctx, query, now.Unix())
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncScheduleRow
	for rows.Next() {
		var r SyncScheduleRow
		var enabled int
		var nextRunAt int64
		var lastRunAt, lastRunID sql.NullInt64
		var createdAt, updatedAt int64
		if err := rows.Scan(&r.ID, &r.TaskID, &r.CronExpr, &r.Timezone, &enabled, &nextRunAt, &lastRunAt, &lastRunID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		r.NextRunAt = time.Unix(nextRunAt, 0).UTC()
		if lastRunAt.Valid {
			t := time.Unix(lastRunAt.Int64, 0).UTC()
			r.LastRunAt = &t
		}
		if lastRunID.Valid {
			id := lastRunID.Int64
			r.LastRunID = &id
		}
		r.CreatedAt = time.Unix(createdAt, 0).UTC()
		r.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// SyncRunItemRow is the SQL-side view of one sync_run_items row
// (v0.6.16). One row per (repo, tag) the engine attempted during a run.
// Times are Unix seconds (UTC); nullable fields use sql.Null* scan
// types. Bytes mirror pull_jobs so the size column is meaningful for
// both pull and push.
//
// state matches the CHECK constraint on the column:
//   - "succeeded" — copy completed
//   - "failed"    — copy errored; Error field carries the reason
//   - "cancelled" — run aborted mid-iteration; currently never written
//                  because the engine returns from pullRepo/pushRepo
//                  before writing per-tag rows on cancellation
type SyncRunItemRow struct {
	ID         int64
	RunID      int64
	Repository string
	Tag        string
	State      string
	Error      string
	BytesDone  int64
	BytesTotal int64
	StartedAt  time.Time
	FinishedAt *time.Time
}

// SyncRunItemCreate inserts one (repo, tag) detail row. Called by the
// engine once per pullTag / pushTag attempt, so a run with N tags
// generates N rows. Errors are logged + swallowed at the engine level
// (per-item row failure must NOT abort a healthy sync — same contract
// as SyncRunUpdateProgress).
func (d *Db) SyncRunItemCreate(ctx context.Context, r SyncRunItemRow) error {
	var finishedAt *int64
	if r.FinishedAt != nil {
		v := r.FinishedAt.Unix()
		finishedAt = &v
	}
	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO sync_run_items(
			run_id, repository, tag, state, error,
			bytes_done, bytes_total, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.RunID, r.Repository, r.Tag, r.State, r.Error,
		r.BytesDone, r.BytesTotal, r.StartedAt.Unix(), finishedAt)
	return err
}

// SyncRunItemListByRun returns items for a run ordered by id ASC —
// this matches the order the engine wrote them, which is the natural
// "first repo, first tag, then next tag, then next repo" iteration
// order, so the UI's expanded detail renders chronologically without a
// separate sort. limit+offset support pagination; total is returned as
// a separate count(*) query so the UI can render "共 N 条 / 第 1 页"
// even when only one page is loaded.
//
// For very large runs the count(*) is cheap thanks to the
// sync_run_items_run index — it's a covering scan of the index leaves
// only, no row reads.
func (d *Db) SyncRunItemListByRun(ctx context.Context, runID int64, limit, offset int) ([]SyncRunItemRow, int, error) {
	var total int
	if err := d.conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_run_items WHERE run_id = ?`,
		runID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	rows, err := d.conn.QueryContext(ctx, `
		SELECT id, run_id, repository, tag, state, error,
		       bytes_done, bytes_total, started_at, finished_at
		FROM sync_run_items WHERE run_id = ?
		ORDER BY id ASC
		LIMIT ? OFFSET ?
	`, runID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []SyncRunItemRow
	for rows.Next() {
		var r SyncRunItemRow
		var startedAt int64
		var finishedAt sql.NullInt64
		if err := rows.Scan(&r.ID, &r.RunID, &r.Repository, &r.Tag, &r.State, &r.Error,
			&r.BytesDone, &r.BytesTotal, &startedAt, &finishedAt); err != nil {
			return nil, 0, err
		}
		r.StartedAt = time.Unix(startedAt, 0).UTC()
		if finishedAt.Valid {
			t := time.Unix(finishedAt.Int64, 0).UTC()
			r.FinishedAt = &t
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// SyncRunItemSummaryByRun returns counts of (succeeded, failed,
// cancelled) for one run. Useful as a server-side pre-aggregation so
// the UI doesn't have to walk the full items list to render a summary
// row — items are loaded lazily on row expand. Mirrors the
// repos_total/repos_synced/repos_failed split on sync_runs but at
// per-item granularity.
//
// Returns zeros when the run has no items yet (engine still iterating,
// or an older run written before this migration).
func (d *Db) SyncRunItemSummaryByRun(ctx context.Context, runID int64) (succeeded, failed, cancelled int, err error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT state, COUNT(*) FROM sync_run_items
		WHERE run_id = ? GROUP BY state
	`, runID)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return 0, 0, 0, err
		}
		switch state {
		case "succeeded":
			succeeded = n
		case "failed":
			failed = n
		case "cancelled":
			cancelled = n
		}
	}
	return succeeded, failed, cancelled, rows.Err()
}
