package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Chenjinteng/cairn/internal/db"
)

// Store is the persistence layer for sync tasks and runs. It adapts the
// row-shaped db package to the API-shaped sync package, and owns the
// JSON encode/decode of deny_list (the only non-trivial transformation).
//
// One Store per process; Engine holds a reference. All methods are
// safe to call concurrently — db.Db serialises via SQLite's WAL.
type Store struct {
	db *db.Db
}

// NewStore wraps the given db. Returns nil only if db is nil; the
// Store itself is just a thin adapter so no other validation is done.
func NewStore(d *db.Db) *Store {
	return &Store{db: d}
}

// CreateTask persists a new task. The deny_list field is JSON-encoded;
// empty slices round-trip as "[]" so the wire form is unambiguous.
//
// Errors:
//   - sql.ErrNoRows-style constraint violation when name collides
//     (the UNIQUE index on sync_tasks.name is the only hard rule).
func (s *Store) CreateTask(ctx context.Context, t Task) (Task, error) {
	if t.ID == "" {
		return Task{}, fmt.Errorf("sync: task id required")
	}
	if t.Name == "" {
		return Task{}, fmt.Errorf("sync: task name required")
	}
	denyJSON, err := jsonEncodeDenyList(t.DenyList)
	if err != nil {
		return Task{}, err
	}
	if t.Direction == "" {
		t.Direction = DirectionPull
	}
	now := time.Now().UTC().Format(time.RFC3339)
	row := db.SyncTaskRow{
		ID:               t.ID,
		Name:             t.Name,
		Direction:        string(t.Direction),
		SourceURL:        t.SourceURL,
		SourceRepoPrefix: t.SourceRepoPrefix,
		TargetRepoPrefix: t.TargetRepoPrefix,
		DenyList:         denyJSON,
		CredentialID:     t.CredentialID,
		Schedule:         t.Schedule,
		Enabled:          t.Enabled,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.db.CreateSyncTask(ctx, row); err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, t.ID)
}

// GetTask returns (zero, nil) on miss so callers can branch on ID
// without dealing with sql.ErrNoRows.
func (s *Store) GetTask(ctx context.Context, id string) (Task, error) {
	row, err := s.db.GetSyncTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if row.ID == "" {
		return Task{}, nil
	}
	return taskFromRow(row)
}

// ListTasks returns all configured tasks, oldest first. The volume is
// expected to be small (low double digits) — no pagination.
func (s *Store) ListTasks(ctx context.Context) ([]Task, error) {
	rows, err := s.db.ListSyncTasks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(rows))
	for _, r := range rows {
		t, err := taskFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// UpdateTask overwrites the editable fields. last_run_* are preserved
// — they're owned by the engine via UpdateTaskAfterRun, not by the
// REST handler. updated_at is set by the DB layer.
func (s *Store) UpdateTask(ctx context.Context, t Task) (Task, error) {
	if t.ID == "" {
		return Task{}, fmt.Errorf("sync: task id required")
	}
	denyJSON, err := jsonEncodeDenyList(t.DenyList)
	if err != nil {
		return Task{}, err
	}
	if t.Direction == "" {
		t.Direction = DirectionPull
	}
	row := db.SyncTaskRow{
		ID:               t.ID,
		Name:             t.Name,
		Direction:        string(t.Direction),
		SourceURL:        t.SourceURL,
		SourceRepoPrefix: t.SourceRepoPrefix,
		TargetRepoPrefix: t.TargetRepoPrefix,
		DenyList:         denyJSON,
		CredentialID:     t.CredentialID,
		Schedule:         t.Schedule,
		Enabled:          t.Enabled,
	}
	n, err := s.db.UpdateSyncTask(ctx, row)
	if err != nil {
		return Task{}, err
	}
	if n == 0 {
		return Task{}, nil
	}
	return s.GetTask(ctx, t.ID)
}

// DeleteTask removes the task and (via FK cascade) its run history.
// Returns the number of task rows deleted (0 = missing, 1 = ok).
func (s *Store) DeleteTask(ctx context.Context, id string) (int64, error) {
	return s.db.DeleteSyncTask(ctx, id)
}

// RecordRunStart inserts a run row in "running" state with zero counts.
// Used by Engine.Run before any work happens so a crash mid-run still
// leaves a trace.
//
// NOTE: we currently store state as success/failed/skipped only. The
// transient "running" state is implicit (finished_at == ""). This keeps
// the state column a closed set the UI can render with a small enum.
func (s *Store) RecordRunStart(ctx context.Context, r Run) error {
	if r.ID == "" || r.TaskID == "" {
		return errors.New("sync: run id and task id required")
	}
	row := db.SyncRunRow{
		ID:        r.ID,
		TaskID:    r.TaskID,
		State:     string(RunStateSuccess), // provisional; UpdateRunFinish overwrites
		StartedAt: r.StartedAt.UTC().Format(time.RFC3339),
	}
	return s.db.InsertSyncRun(ctx, row)
}

// RecordRunFinish writes the terminal state and counts, then
// denormalises last_run_* onto the task row in one extra UPDATE so
// the list view doesn't have to join sync_runs.
func (s *Store) RecordRunFinish(ctx context.Context, r Run) error {
	if r.State == "" {
		return errors.New("sync: run state required")
	}
	if err := s.db.UpdateSyncRun(ctx, db.SyncRunRow{
		ID:              r.ID,
		State:           string(r.State),
		FinishedAt:      r.FinishedAt.UTC().Format(time.RFC3339),
		ManifestsCopied: r.ManifestsCopied,
		BlobsCopied:     r.BlobsCopied,
		BlobsSkipped:    r.BlobsSkipped,
		BytesTotal:      r.BytesTotal,
		Error:           r.Error,
	}); err != nil {
		return err
	}
	return s.db.UpdateSyncTaskAfterRun(ctx, r.TaskID, string(r.State), r.Error, r.FinishedAt.UTC().Format(time.RFC3339))
}

// ListRuns returns the most recent runs (newest first). limit <= 0
// means "all"; the UI uses a finite bound for paging.
func (s *Store) ListRuns(ctx context.Context, taskID string, limit int) ([]Run, error) {
	rows, err := s.db.ListSyncRuns(ctx, taskID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(rows))
	for _, r := range rows {
		out = append(out, runFromRow(r))
	}
	return out, nil
}

// taskFromRow converts a DB row to the API view, decoding deny_list
// and parsing RFC3339 time strings. A malformed deny_list is treated
// as empty (logged-worthy but not fatal — the engine will skip nothing
// and behave like no filter). Time fields default to zero on parse
// failure (same forgiving contract as pull_jobs history).
func taskFromRow(r db.SyncTaskRow) (Task, error) {
	deny, err := jsonDecodeDenyList(r.DenyList)
	if err != nil {
		return Task{}, fmt.Errorf("sync: task %s: %w", r.ID, err)
	}
	created, _ := time.Parse(time.RFC3339, r.CreatedAt)
	updated, _ := time.Parse(time.RFC3339, r.UpdatedAt)
	return Task{
		ID:               r.ID,
		Name:             r.Name,
		Direction:        Direction(r.Direction),
		SourceURL:        r.SourceURL,
		SourceRepoPrefix: r.SourceRepoPrefix,
		TargetRepoPrefix: r.TargetRepoPrefix,
		DenyList:         deny,
		CredentialID:     r.CredentialID,
		Schedule:         r.Schedule,
		Enabled:          r.Enabled,
		LastRunAt:        r.LastRunAt,
		LastRunStatus:    r.LastRunStatus,
		LastRunError:     r.LastRunError,
		CreatedAt:        created,
		UpdatedAt:        updated,
	}, nil
}

func runFromRow(r db.SyncRunRow) Run {
	started, _ := time.Parse(time.RFC3339, r.StartedAt)
	finished, _ := time.Parse(time.RFC3339, r.FinishedAt)
	return Run{
		ID:              r.ID,
		TaskID:          r.TaskID,
		State:           RunState(r.State),
		StartedAt:       started,
		FinishedAt:      finished,
		ManifestsCopied: r.ManifestsCopied,
		BlobsCopied:     r.BlobsCopied,
		BlobsSkipped:    r.BlobsSkipped,
		BytesTotal:      r.BytesTotal,
		Error:           r.Error,
	}
}

// jsonEncodeDenyList encodes the slice as JSON. nil and empty both
// produce "[]" so the column never holds the literal string "" which
// would silently disable all filtering.
func jsonEncodeDenyList(in []string) (string, error) {
	if in == nil {
		in = []string{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("sync: encode deny_list: %w", err)
	}
	return string(b), nil
}

// jsonDecodeDenyList parses a JSON array of strings. Empty string
// decodes to an empty (non-nil) slice — matches the encode contract.
func jsonDecodeDenyList(s string) ([]string, error) {
	if s == "" {
		return []string{}, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}