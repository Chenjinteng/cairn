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
type SyncTaskRow struct {
	ID             int64
	Name           string
	Direction      string // 'pull' | 'push' — string at this layer, enum upstream
	RemoteURL      string
	RemoteUsername string
	RemotePassword string
	Include        string
	Enabled        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
		INSERT INTO sync_tasks(name, direction, remote_url, remote_username, remote_password, include, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, t.Name, t.Direction, t.RemoteURL, t.RemoteUsername, t.RemotePassword, t.Include, enabled,
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
func (d *Db) SyncTaskGet(ctx context.Context, id int64) (SyncTaskRow, error) {
	var t SyncTaskRow
	var enabled int
	var createdAt, updatedAt int64
	err := d.conn.QueryRowContext(ctx, `
		SELECT id, name, direction, remote_url, remote_username, remote_password, include, enabled, created_at, updated_at
		FROM sync_tasks WHERE id = ?
	`, id).Scan(&t.ID, &t.Name, &t.Direction, &t.RemoteURL, &t.RemoteUsername, &t.RemotePassword, &t.Include, &enabled,
		&createdAt, &updatedAt)
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
		SELECT id, name, direction, remote_url, remote_username, remote_password, include, enabled, created_at, updated_at
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
		if err := rows.Scan(&t.ID, &t.Name, &t.Direction, &t.RemoteURL, &t.RemoteUsername, &t.RemotePassword, &t.Include, &enabled,
			&createdAt, &updatedAt); err != nil {
			return nil, err
		}
		t.Enabled = enabled != 0
		t.CreatedAt = time.Unix(createdAt, 0).UTC()
		t.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

// SyncTaskUpdate replaces name/direction/url/credentials/include/enabled
// for the given ID. created_at is preserved; updated_at is overwritten.
func (d *Db) SyncTaskUpdate(ctx context.Context, t SyncTaskRow) error {
	enabled := 0
	if t.Enabled {
		enabled = 1
	}
	res, err := d.conn.ExecContext(ctx, `
		UPDATE sync_tasks SET name=?, direction=?, remote_url=?, remote_username=?, remote_password=?, include=?, enabled=?, updated_at=?
		WHERE id=?
	`, t.Name, t.Direction, t.RemoteURL, t.RemoteUsername, t.RemotePassword, t.Include, enabled,
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
		INSERT INTO sync_runs(task_id, started_at, finished_at, status, repos_total, repos_synced, repos_failed, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, r.TaskID, r.StartedAt.Unix(), finishedAt, r.Status,
		r.ReposTotal, r.ReposSynced, r.ReposFailed, r.Error)
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

// SyncRunListByTask returns runs for a task, newest first. limit<=0
// means no limit; the UI passes 50 (recent runs only — older history is
// eventual v0.6.2+ retention cleanup territory).
func (d *Db) SyncRunListByTask(ctx context.Context, taskID int64, limit int) ([]SyncRunRow, error) {
	query := `
		SELECT id, task_id, started_at, finished_at, status, repos_total, repos_synced, repos_failed, error
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
			&r.ReposTotal, &r.ReposSynced, &r.ReposFailed, &r.Error); err != nil {
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