package sync

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Scheduler fires SyncTasks on cron schedules stored in sync_schedules.
// v0.6.11.
//
// One ticker per process (interval: 30s) queries enabled rows whose
// next_run_at <= now and dispatches each through the same Engine.Start
// entry used by the HTTP "run now" button. That means every
// guarantee v0.6.8 SYNC-1/4 added — TryLock (no double-fire), detached
// context (request lifetime doesn't cancel the run), per-run recover —
// applies identically to scheduled runs.
//
// The ticker beats per-schedule goroutines for two reasons:
//   1. Memory: N schedules need O(1) goroutines, not O(N).
//   2. State: a fresh boot reads the DB once and picks up where the
//      process left off (the same NextRunAt is still in the future for
//      schedules the previous process never got to fire).
//
// The loop holds the runtime context (r.PullCtx) for cancellation only;
// every DB write uses its own short-lived context so a torn-down ctx
// doesn't lose a stamp or a fire.
type Scheduler struct {
	store  *Store
	engine *Engine
	log    *slog.Logger
}

// NewScheduler builds a Scheduler. log may be nil (defaults to
// slog.Default()).
func NewScheduler(store *Store, engine *Engine, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{store: store, engine: engine, log: log}
}

// Run loops until ctx is cancelled. First pass fires 5 seconds after
// startup (so a fresh boot doesn't immediately try to catch up on
// schedules the previous process let slip) and then every 30s.
//
// Each pass:
//
//  1. ListDue(s) — all enabled schedules whose next_run_at <= now.
//  2. For each: Engine.Start(task). Per-task TryLock (v0.6.8 SYNC-1)
//     means a concurrent "run now" click or a double-fire on the same
//     minute becomes a 409-shaped no-op — the loop just moves on.
//  3. ScheduleUpdateAfterFire stamps last_run_at / last_run_id and
//     advances next_run_at to the next cron boundary after now.
//
// Failures inside the loop are logged and swallowed — the loop must
// survive a single bad schedule, bad task, or transient DB error.
func (s *Scheduler) Run(ctx context.Context) {
	const (
		firstDelay = 5 * time.Second
		interval   = 30 * time.Second
	)
	timer := time.NewTimer(firstDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		s.tick(ctx)
		timer.Reset(interval)
	}
}

// tick runs one scan + dispatch cycle. Extracted so a future test can
// drive the loop deterministically.
func (s *Scheduler) tick(ctx context.Context) {
	now := time.Now().UTC()
	scanCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	schedules, err := s.store.ScheduleListDue(scanCtx, now, 100)
	cancel()
	if err != nil {
		s.log.Warn("scheduler: list due failed", "err", err)
		return
	}
	if len(schedules) == 0 {
		return
	}
	for i := range schedules {
		sched := &schedules[i]
		if ctx.Err() != nil {
			return
		}
		s.fireOne(ctx, sched)
	}
}

// fireOne loads the task and dispatches it through Engine.Start.
// Failures after the schedule row is read (task gone, task disabled,
// engine busy, panic) are logged and the schedule's last_run_at /
// next_run_at are still updated — except for panics, which leave the
// row alone so the next tick re-tries.
func (s *Scheduler) fireOne(ctx context.Context, sched *Schedule) {
	// Wrap the actual work so a panic doesn't kill the loop.
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("scheduler: panic firing schedule",
				"schedule_id", sched.ID, "task_id", sched.TaskID, "panic", r)
		}
	}()

	taskCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	task, err := s.store.GetTask(taskCtx, sched.TaskID)
	cancel()
	if err != nil {
		s.log.Warn("scheduler: load task failed",
			"schedule_id", sched.ID, "task_id", sched.TaskID, "err", err)
		// Advance next_run_at so we don't hammer the DB with the same miss
		// every 30s. The cron "next" still moves forward.
		s.advanceSchedule(sched)
		return
	}

	if !task.Enabled {
		s.log.Info("scheduler: skip disabled task",
			"schedule_id", sched.ID, "task_id", sched.TaskID)
		s.advanceSchedule(sched)
		return
	}

	// Detached context: scheduler loop ctx could be cancelled, but the
	// run must survive until completion. Engine.Start spawns its own
	// goroutine; we just need to feed it a valid task snapshot.
	runCtx, runCancel := context.WithTimeout(context.Background(), 5*time.Second)
	run, err := s.engine.Start(runCtx, task)
	runCancel()
	if err != nil {
		// TryLock conflict → another run is in flight; harmless, the
		// schedule is still on time. Advance next_run_at so we don't
		// retry this same minute.
		if errors.Is(err, ErrTaskRunning) || errors.Is(err, ErrTaskDisabled) {
			s.log.Info("scheduler: skip",
				"schedule_id", sched.ID, "task_id", sched.TaskID, "reason", err.Error())
			s.advanceSchedule(sched)
			return
		}
		s.log.Warn("scheduler: start failed",
			"schedule_id", sched.ID, "task_id", sched.TaskID, "err", err)
		s.advanceSchedule(sched)
		return
	}

	s.log.Info("scheduler: fired",
		"schedule_id", sched.ID, "task_id", sched.TaskID, "run_id", run.ID)

	// Stamp last_run_at / last_run_id and advance next_run_at. Use a
	// short-lived context so the loop's cancel can't kill a DB write.
	if err := s.store.ScheduleUpdateAfterFire(context.Background(), sched, run.ID); err != nil {
		s.log.Warn("scheduler: post-fire update failed",
			"schedule_id", sched.ID, "task_id", sched.TaskID, "err", err)
	}
}

// advanceSchedule pushes next_run_at forward to the next cron boundary
// after now, without touching last_run_at / last_run_id. Used on the
// "skip" paths (task missing / disabled / busy) so the loop doesn't
// re-attempt the same minute every tick.
func (s *Scheduler) advanceSchedule(sched *Schedule) {
	next, err := NextAfter(sched.CronExpr, sched.Timezone, time.Now().UTC())
	if err != nil {
		s.log.Warn("scheduler: advance failed",
			"schedule_id", sched.ID, "task_id", sched.TaskID, "err", err)
		return
	}
	sched.NextRunAt = next.UTC()
	sched.UpdatedAt = time.Now().UTC()
	updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.db.SyncScheduleUpdate(updateCtx, scheduleToRow(*sched)); err != nil {
		s.log.Warn("scheduler: persist advance failed",
			"schedule_id", sched.ID, "task_id", sched.TaskID, "err", err)
	}
}