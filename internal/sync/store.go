package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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

// MaxRunsPerTask is the per-task run-history retention cap. v0.6.30.
//
// sync_runs has no auto-cleanup mechanism — runs accumulated forever
// before 0.6.30, with the UI only fetching the latest 50 for display
// (Store.ListRunsByTask default). The remaining rows were dead weight
// in the DB and made the per-task history unbounded.
//
// After every CreateRun, the engine trims to keep only the most recent
// MaxRunsPerTask runs for that task; ON DELETE CASCADE on
// sync_run_items(run_id) carries the per-tag detail rows along.
//
// 10 is empirically "enough to spot a recent trend" (the UI lists them
// newest-first; a typical user complaint is "the last 3-4 ran fine"),
// small enough that a chatty / hourly task doesn't grow the DB forever.
const MaxRunsPerTask = 10

// CreateRun records a run start. The engine calls this once at the
// beginning of iteration. Sets r.ID in place.
//
// Status must be one of the four valid values; anything else returns
// ErrInvalidStatus without touching the DB.
//
// v0.6.30: after a successful insert, trims the per-task history down to
// MaxRunsPerTask. Failure to trim is logged at WARN and swallowed — the
// new run still completes; trim is best-effort.
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

	// v0.6.30: bound the per-task history. Best-effort — failures are
	// logged but don't abort the new run. The trim target uses MaxRunsPerTask,
	// not the UI's historical 50; see MaxRunsPerTask doc for the rationale.
	if _, err := s.TrimRuns(ctx, r.TaskID, MaxRunsPerTask); err != nil {
		// Don't propagate: trim is housekeeping; the run already succeeded.
		// Log here so operators can spot repeated failures (a stuck trim
		// would let the table grow unboundedly).
		slog.Default().Warn("sync: trim old runs failed",
			"task_id", r.TaskID,
			"keep", MaxRunsPerTask,
			"err", err.Error())
	}
	return nil
}

// TrimRuns is the public wrapper over db.SyncRunTrimOlder — keeps the
// newest `keep` runs for a task, deletes the rest. Returns the number of
// rows actually deleted (0 is fine — a task with <= keep runs does no
// deletes). v0.6.30.
//
// `keep <= 0` is a no-op (defensive — caller passed an uninitialized
// config or similar). Never means "delete all".
func (s *Store) TrimRuns(ctx context.Context, taskID int64, keep int) (int64, error) {
	return s.db.SyncRunTrimOlder(ctx, taskID, keep)
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

// ErrScheduleNotFound is returned by store.ScheduleGet / ScheduleDelete
// when the id doesn't exist. Handlers translate to 404.
var ErrScheduleNotFound = errors.New("sync: schedule not found")

// --- schedule CRUD (v0.6.11) ---------------------------------------------

// rowToSchedule converts a SyncScheduleRow to the domain Schedule.
func rowToSchedule(r db.SyncScheduleRow) Schedule {
	return Schedule{
		ID:        r.ID,
		TaskID:    r.TaskID,
		CronExpr:  r.CronExpr,
		Timezone:  r.Timezone,
		Enabled:   r.Enabled,
		NextRunAt: r.NextRunAt,
		LastRunAt: r.LastRunAt,
		LastRunID: r.LastRunID,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

// scheduleToRow converts a domain Schedule to a SyncScheduleRow.
func scheduleToRow(s Schedule) db.SyncScheduleRow {
	return db.SyncScheduleRow{
		ID:        s.ID,
		TaskID:    s.TaskID,
		CronExpr:  s.CronExpr,
		Timezone:  s.Timezone,
		Enabled:   s.Enabled,
		NextRunAt: s.NextRunAt,
		LastRunAt: s.LastRunAt,
		LastRunID: s.LastRunID,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
	}
}

// ScheduleCreate validates the schedule and inserts a new row. Caller
// must have set TaskID; ID / CreatedAt / UpdatedAt are filled in place.
// Validation errors (bad cron / bad timezone / empty cron) return the
// same sentinel errors as Schedule.Validate so handlers can errors.Is
// without unwrapping.
func (s *Store) ScheduleCreate(ctx context.Context, sched *Schedule) error {
	now := time.Now().UTC()
	if err := sched.Validate(now); err != nil {
		return err
	}
	sched.CreatedAt = now
	sched.UpdatedAt = now
	id, err := s.db.SyncScheduleCreate(ctx, scheduleToRow(*sched))
	if err != nil {
		return err
	}
	sched.ID = id
	return nil
}

// ScheduleUpdate re-validates the schedule (caller may have changed
// CronExpr / Timezone / Enabled), refreshes NextRunAt, and updates the
// row. updated_at is bumped to now.
func (s *Store) ScheduleUpdate(ctx context.Context, sched *Schedule) error {
	now := time.Now().UTC()
	if err := sched.Validate(now); err != nil {
		return err
	}
	sched.UpdatedAt = now
	err := s.db.SyncScheduleUpdate(ctx, scheduleToRow(*sched))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrScheduleNotFound
		}
		return err
	}
	return nil
}

// ScheduleDelete removes a schedule by id. Returns ErrScheduleNotFound
// when the id doesn't exist. The task itself is unaffected — schedules
// are independent of the task lifecycle (vs. cascade-delete on
// task_id at the FK level which fires only when the task is dropped).
func (s *Store) ScheduleDelete(ctx context.Context, id int64) error {
	err := s.db.SyncScheduleDelete(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrScheduleNotFound
	}
	return err
}

// ScheduleListByTask returns every schedule for a task, oldest first.
// Used by the UI's drawer Tab. limit <= 0 means no limit.
func (s *Store) ScheduleListByTask(ctx context.Context, taskID int64, limit int) ([]Schedule, error) {
	rows, err := s.db.SyncScheduleListByTask(ctx, taskID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Schedule, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToSchedule(r))
	}
	return out, nil
}

// ScheduleListDue returns every enabled schedule whose next_run_at <=
// now. Called by the scheduler loop every 30s. Returns at most `limit`
// rows; the loop processes them and the next tick catches the rest.
func (s *Store) ScheduleListDue(ctx context.Context, now time.Time, limit int) ([]Schedule, error) {
	rows, err := s.db.SyncScheduleListDue(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Schedule, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToSchedule(r))
	}
	return out, nil
}

// ScheduleUpdateAfterFire is called by the scheduler right after firing
// a run: stamps last_run_at / last_run_id and recomputes next_run_at
// so the loop won't fire again until the next cron boundary. Failures
// are logged by the scheduler (progress-style: NOT fatal; a missed
// stamp just means the next tick may re-fire on the same minute — the
// per-task TryLock makes that a no-op).
func (s *Store) ScheduleUpdateAfterFire(ctx context.Context, sched *Schedule, lastRunID int64) error {
	now := time.Now().UTC()
	sched.LastRunAt = &now
	sched.LastRunID = &lastRunID
	sched.UpdatedAt = now
	next, err := NextAfter(sched.CronExpr, sched.Timezone, now)
	if err != nil {
		// Should not happen — we validated on create / update. If it does,
		// leave the old NextRunAt in place and let the operator fix the
		// cron expression.
		return fmt.Errorf("recompute next_run_at: %w", err)
	}
	sched.NextRunAt = next.UTC()
	return s.db.SyncScheduleUpdate(ctx, scheduleToRow(*sched))
}
// --- run items (v0.6.16) ----------------------------------------------------

// CreateRunItem inserts one (repo, tag) detail row for an in-flight
// run. Called by the engine from inside pullTag / pushTag — once per
// attempt, after the attempt terminates. The caller has already
// determined State ("succeeded" / "failed") and Error ("" on
// success). StartedAt is the attempt's start time; FinishedAt is the
// attempt's end. The engine takes time.Now() UTC at the right two
// points so it can stamp duration without holding timestamps in a
// variable across function calls.
//
// Errors are NOT swallowed here — they bubble back to the engine,
// which logs at WARN and continues. A failure to write a per-item row
// must NOT abort a healthy sync; this is consistent with
// UpdateRunProgress (which writes on every transition).
func (s *Store) CreateRunItem(ctx context.Context, item SyncRunItem) error {
	return s.db.SyncRunItemCreate(ctx, db.SyncRunItemRow{
		RunID:      item.RunID,
		Repository: item.Repository,
		Tag:        item.Tag,
		State:      item.State,
		Error:      item.Error,
		BytesDone:  item.BytesDone,
		BytesTotal: item.BytesTotal,
		StartedAt:  item.StartedAt,
		FinishedAt: item.FinishedAt,
	})
}

// ListRunItems returns one page of (repo, tag) attempts for a run,
// ordered by insertion time (engine iteration order). limit<=0 means
// "no limit" — used internally for tests. The handler always passes a
// positive limit so the API is bounded.
//
// total is the unpaged count so the UI can render "共 N 条" without a
// separate round trip. When total==0 the returned slice is nil.
func (s *Store) ListRunItems(ctx context.Context, runID int64, limit, offset int) ([]SyncRunItem, int, error) {
	rows, total, err := s.db.SyncRunItemListByRun(ctx, runID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	if rows == nil {
		return nil, 0, nil
	}
	out := make([]SyncRunItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, itemRowToDomain(r))
	}
	return out, total, nil
}

// RunItemSummary returns the per-state count breakdown for a run.
// Cheap (single grouped COUNT(*) on the run_id index) — meant to be
// called by handlers that want to enrich a SyncRun response without
// forcing the UI to load the full items list.
func (s *Store) RunItemSummary(ctx context.Context, runID int64) (SyncRunItemSummary, error) {
	succ, fail, canc, err := s.db.SyncRunItemSummaryByRun(ctx, runID)
	if err != nil {
		return SyncRunItemSummary{}, err
	}
	return SyncRunItemSummary{Succeeded: succ, Failed: fail, Cancelled: canc}, nil
}

// itemRowToDomain converts a db.SyncRunItemRow into the domain
// SyncRunItem. Trivial today; the indirection matches the rowToRun /
// rowToTask pattern in this file and gives a single place to add
// normalization later (e.g. trimming tag whitespace, normalizing
// state casing).
func itemRowToDomain(r db.SyncRunItemRow) SyncRunItem {
	return SyncRunItem{
		ID:         r.ID,
		RunID:      r.RunID,
		Repository: r.Repository,
		Tag:        r.Tag,
		State:      r.State,
		Error:      r.Error,
		BytesDone:  r.BytesDone,
		BytesTotal: r.BytesTotal,
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
	}
}
