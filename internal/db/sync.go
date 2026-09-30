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

// SyncTaskCreate inserts a new task and returns the assigned ID. UNIQUE
// constraint violations (name conflict) bubble up as raw SQLite errors;
// the handler layer matches on the error message and translates to 409.
func (d *Db) SyncTaskCreate(ctx context.Context, t SyncTaskRow) (int64, error) {
	enabled := 0
	if t.Enabled {
		enabled = 1
	}
	res, err := d.conn.ExecContext(ctx, `
		INSERT INTO sync_tasks(name, direction, remote_url, remote_username, remote_password, remote_credential_id, include, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, t.Name, t.Direction, t.RemoteURL, t.RemoteUsername, t.RemotePassword, t.RemoteCredentialID, t.Include, enabled,
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
		SELECT id, name, direction, remote_url, remote_username, remote_password, remote_credential_id, include, enabled, created_at, updated_at,
		       COALESCE((SELECT r.status       FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), ''),
		       COALESCE((SELECT r.id           FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), 0),
		       COALESCE((SELECT r.current_repo FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), ''),
		       COALESCE((SELECT r.current_tag  FROM sync_runs r WHERE r.task_id = sync_tasks.id ORDER BY r.started_at DESC, r.id DESC LIMIT 1), '')
		FROM sync_tasks WHERE id = ?
	`, id).Scan(&t.ID, &t.Name, &t.Direction, &t.RemoteURL, &t.RemoteUsername, &t.RemotePassword, &t.RemoteCredentialID, &t.Include, &enabled,
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
		SELECT id, name, direction, remote_url, remote_username, remote_password, remote_credential_id, include, enabled, created_at, updated_at,
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
		if err := rows.Scan(&t.ID, &t.Name, &t.Direction, &t.RemoteURL, &t.RemoteUsername, &t.RemotePassword, &t.RemoteCredentialID, &t.Include, &enabled,
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
		UPDATE sync_tasks SET name=?, direction=?, remote_url=?, remote_username=?, remote_password=?, remote_credential_id=?, include=?, enabled=?, updated_at=?
		WHERE id=?
	`, t.Name, t.Direction, t.RemoteURL, t.RemoteUsername, t.RemotePassword, t.RemoteCredentialID, t.Include, enabled,
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
// means no limit; the UI passes 50 (recent runs only — older history is
// eventual v0.6.2+ retention cleanup territory).
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