package sync

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Chenjinteng/cairn/internal/db"
)

// Store wraps *db.Db with sync-specific CRUD that operates on domain
// types (SyncTask / SyncRun from types.go) instead of the SQL row types.
// Handlers and the engine should use Store instead of touching *db.Db
// directly — it centralizes:
//   - row ↔ domain conversion (Direction enum, time.Time round-trip)
//   - UNIQUE constraint → ErrTaskNameConflict translation (so handlers
//     can errors.Is() instead of grepping sqlite error strings)
//   - sql.ErrNoRows → ErrTaskNotFound / ErrRunNotFound translation
type Store struct {
	db *db.Db
}

// NewStore constructs a Store backed by the given db handle. The handle
// is borrowed; Store does not take ownership and does not Close it.
func NewStore(database *db.Db) *Store {
	return &Store{db: database}
}

// CreateTask inserts a new task. Sets t.ID, t.CreatedAt, t.UpdatedAt in
// place. The caller must have validated t (see SyncTask.Validate).
//
// Returns ErrTaskNameConflict if a task with the same name already
// exists (UNIQUE constraint on sync_tasks.name).
func (s *Store) CreateTask(ctx context.Context, t *SyncTask) error {
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	id, err := s.db.SyncTaskCreate(ctx, db.SyncTaskRow{
		Name:               t.Name,
		Direction:          string(t.Direction),
		RemoteURL:          t.RemoteURL,
		RemoteUsername:     t.RemoteUsername,
		RemotePassword:     t.RemotePassword,
		RemoteCredentialID: t.RemoteCredentialID,
		Include:            t.Include,
		Enabled:            t.Enabled,
		CreatedAt:          t.CreatedAt,
		UpdatedAt:          t.UpdatedAt,
	})
	if err != nil {
		if isUniqueNameConflict(err) {
			return ErrTaskNameConflict
		}
		return err
	}
	t.ID = id
	return nil
}

// UpdateTask applies a full replacement of an existing task. Caller
// must have validated t (see SyncTask.Validate) and set t.ID from a
// prior Get. updated_at is overwritten to time.Now.UTC().
//
// Returns ErrTaskNotFound if the ID doesn't exist, ErrTaskNameConflict
// if the new name collides with another task.
func (s *Store) UpdateTask(ctx context.Context, t *SyncTask) error {
	t.UpdatedAt = time.Now().UTC()
	err := s.db.SyncTaskUpdate(ctx, db.SyncTaskRow{
		ID:                 t.ID,
		Name:               t.Name,
		Direction:          string(t.Direction),
		RemoteURL:          t.RemoteURL,
		RemoteUsername:     t.RemoteUsername,
		RemotePassword:     t.RemotePassword,
		RemoteCredentialID: t.RemoteCredentialID,
		Include:            t.Include,
		Enabled:            t.Enabled,
		CreatedAt:          t.CreatedAt,
		UpdatedAt:          t.UpdatedAt,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTaskNotFound
		}
		if isUniqueNameConflict(err) {
			return ErrTaskNameConflict
		}
		return err
	}
	return nil
}

// DeleteTask removes a task and (via FK CASCADE) all its runs.
// Returns ErrTaskNotFound if the ID doesn't exist.
func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	err := s.db.SyncTaskDelete(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaskNotFound
	}
	return err
}

// GetTask returns one task by ID. Returns ErrTaskNotFound if absent.
func (s *Store) GetTask(ctx context.Context, id int64) (SyncTask, error) {
	row, err := s.db.SyncTaskGet(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SyncTask{}, ErrTaskNotFound
		}
		return SyncTask{}, err
	}
	return rowToTask(row), nil
}

// ListTasks returns every task, newest first. The credential fields
// (RemoteUsername + RemotePassword) are populated (callers — i.e. the
// engine — need them to authenticate to the remote); the API layer
// must NOT serialize RemotePassword directly to clients (see the
// json:"-" tag on SyncTask.RemotePassword).
func (s *Store) ListTasks(ctx context.Context) ([]SyncTask, error) {
	rows, err := s.db.SyncTaskList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SyncTask, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToTask(r))
	}
	return out, nil
}

// CreateRun records a run start. The engine calls this once at the
// beginning of iteration. Sets r.ID in place.
//
// Status must be one of the four valid values; anything else returns
// ErrInvalidStatus without touching the DB.
func (s *Store) CreateRun(ctx context.Context, r *SyncRun) error {
	if !r.Status.Valid() {
		return ErrInvalidStatus
	}
	id, err := s.db.SyncRunCreate(ctx, db.SyncRunRow{
		TaskID:      r.TaskID,
		StartedAt:   r.StartedAt,
		FinishedAt:  r.FinishedAt,
		Status:      string(r.Status),
		ReposTotal:  r.ReposTotal,
		ReposSynced: r.ReposSynced,
		ReposFailed: r.ReposFailed,
		Error:       r.Error,
		CurrentRepo: r.CurrentRepo,
		CurrentTag:  r.CurrentTag,
	})
	if err != nil {
		return err
	}
	r.ID = id
	return nil
}

// UpdateRun applies the terminal state when iteration ends. The engine
// fills r.Status / r.FinishedAt / counters / r.Error before calling.
// task_id is immutable.
//
// Returns ErrRunNotFound if the ID doesn't exist, ErrInvalidStatus
// if the status value isn't recognized.
func (s *Store) UpdateRun(ctx context.Context, r SyncRun) error {
	if !r.Status.Valid() {
		return ErrInvalidStatus
	}
	err := s.db.SyncRunUpdate(ctx, db.SyncRunRow{
		ID:          r.ID,
		TaskID:      r.TaskID,
		StartedAt:   r.StartedAt,
		FinishedAt:  r.FinishedAt,
		Status:      string(r.Status),
		ReposTotal:  r.ReposTotal,
		ReposSynced: r.ReposSynced,
		ReposFailed: r.ReposFailed,
		Error:       r.Error,
		CurrentRepo: r.CurrentRepo,
		CurrentTag:  r.CurrentTag,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return err
	}
	return nil
}

// UpdateRunProgress is called by the engine on every (repo, tag) transition
// during iteration. It writes ONLY current_repo and current_tag — counters
// and terminal fields stay untouched so concurrent reads from the UI poll
// never see a partially-flushed snapshot. Cheap (single-row UPDATE) so it
// can fire hundreds of times per run without blocking.
//
// Pass empty strings to clear (e.g. engine wants to mark "starting repo
// enumeration" without naming a specific repo).
func (s *Store) UpdateRunProgress(ctx context.Context, runID int64, repo, tag string) error {
	return s.db.SyncRunUpdateProgress(ctx, runID, repo, tag)
}

// ListRunsByTask returns up to `limit` runs for a task, newest first.
// limit<=0 means "all" — UI passes 50 by convention; older history is
// v0.6.2+ retention cleanup territory.
func (s *Store) ListRunsByTask(ctx context.Context, taskID int64, limit int) ([]SyncRun, error) {
	rows, err := s.db.SyncRunListByTask(ctx, taskID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]SyncRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToRun(r))
	}
	return out, nil
}

// MarkStaleRunsFailed flips every sync_runs row still in 'running' to
// 'failed' with the given reason, returning how many rows were touched.
// Called once at process startup: any 'running' row at boot time is by
// definition a zombie (the previous process died mid-run — a synchronous
// or async run cannot survive a restart), so this closes the books on
// them instead of leaving the UI showing a forever-spinning task.
//
// This is the SYNC-1/SYNC-4 counterpart to the engine's detached-context
// design: detached ctx keeps runs alive across the HTTP request lifetime,
// this cleans up after the process itself dies.
func (s *Store) MarkStaleRunsFailed(ctx context.Context, finishedAt time.Time, reason string) (int64, error) {
	return s.db.SyncRunMarkRunningFailed(ctx, finishedAt, reason)
}

// --- converters (private) --------------------------------------------------

func rowToTask(r db.SyncTaskRow) SyncTask {
	return SyncTask{
		ID:                 r.ID,
		Name:               r.Name,
		Direction:          Direction(r.Direction),
		RemoteURL:          r.RemoteURL,
		RemoteUsername:     r.RemoteUsername,
		RemotePassword:     r.RemotePassword,
		RemoteCredentialID: r.RemoteCredentialID,
		Include:            r.Include,
		Enabled:            r.Enabled,
		LastRunStatus:      SyncRunStatus(r.LastRunStatus),
		LastRunCurrentRepo: r.LastRunCurrentRepo,
		LastRunCurrentTag:  r.LastRunCurrentTag,
		CreatedAt:          r.CreatedAt,
		UpdatedAt:          r.UpdatedAt,
	}
}

func rowToRun(r db.SyncRunRow) SyncRun {
	return SyncRun{
		ID:          r.ID,
		TaskID:      r.TaskID,
		StartedAt:   r.StartedAt,
		FinishedAt:  r.FinishedAt,
		Status:      SyncRunStatus(r.Status),
		ReposTotal:  r.ReposTotal,
		ReposSynced: r.ReposSynced,
		ReposFailed: r.ReposFailed,
		Error:       r.Error,
		CurrentRepo: r.CurrentRepo,
		CurrentTag:  r.CurrentTag,
	}
}

// isUniqueNameConflict reports whether err is a SQLite UNIQUE
// constraint violation on sync_tasks.name. modernc.org/sqlite doesn't
// expose typed error codes, so we match on the message prefix — this
// string has been stable since the migration was introduced.
func isUniqueNameConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: sync_tasks.name")
}