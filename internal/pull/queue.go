// Package pull implements the FIFO pull queue and the orchestration that
// downloads an image from a source registry and pushes it into the managed
// destination registry.
//
// Design (mirrors registry-manager/server/puller.mjs):
//   - Single concurrent: one PullJob holds the executor; others queue.
//   - Lifecycle: queued → running → succeeded | failed | cancelled.
//   - Cancel is cooperative: a cancel flips a flag the streaming reads check
//     between chunks; the in-flight PATCH is allowed to complete so we don't
//     leave the destination registry with a half-uploaded blob.
//   - Job state is in memory; on restart, history stays in SQLite
//     (REGISTRY_PULL_HISTORY_RETENTION_DAYS) but the live queue resets.
package pull

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Chenjinteng/cairn/internal/db"
)

// JobState is the lifecycle of a pull job.
//
// Allowed transitions:
//
//	queued     → running     (executor picks it up)
//	running    → succeeded  (all blobs uploaded + manifest PUT OK)
//	running    → failed     (any step returned an error)
//	running    → cancelled  (Cancel() called mid-flight)
//	queued     → cancelled  (Cancel() called before executor picked it up)
type JobState string

const (
	StateQueued    JobState = "queued"
	StateRunning   JobState = "running"
	StateSucceeded JobState = "succeeded"
	StateFailed    JobState = "failed"
	StateCancelled JobState = "cancelled"
)

// Phase status values for Phase.Status. The UI (web/src/types.ts
// PullPhaseStatus) renders each with its own color; 'skipped' is for blobs
// the destination already had.
const (
	PhasePending = "pending"
	PhaseRunning = "running"
	PhaseSuccess = "success"
	PhaseFailed  = "failed"
	PhaseSkipped = "skipped"
)

// Phase is one visible step of a pull job, mirroring the UI's PullPhase
// (web/src/types.ts): manifest / config / blob:<i> (rendered "blob #i") /
// child-manifests. TotalBytes is nil when the source advertised no size —
// the UI shows a bare byte count instead of a fake "x / 0".
type Phase struct {
	Name       string `json:"name"`
	Digest     string `json:"digest"`
	Status     string `json:"status"`
	Bytes      int64  `json:"bytes"`
	TotalBytes *int64 `json:"totalBytes"`
	Message    string `json:"message"`
}

// JobView is the lock-free, copyable projection of a Job. Same JSON tags
// as Job but without the mutex, so it can live in slices and API responses.
//
// Mutations to the live Job never leak out: handlers always receive JobView.
type JobView struct {
	ID         string    `json:"id"`
	SourceRef  string    `json:"sourceRef"`
	DestRepo   string    `json:"destRepo"`
	DestTag    string    `json:"destTag"`
	Credential string    `json:"credential"`
	Proxy      string    `json:"proxy"`
	State      JobState  `json:"state"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	EndedAt    time.Time `json:"endedAt,omitempty"`
	Error      string    `json:"error,omitempty"`
	BytesDone  int64     `json:"bytesDone"`
	BytesTotal int64     `json:"bytesTotal"`
	BlobsDone  int       `json:"blobsDone"`
	BlobsTotal int       `json:"blobsTotal"`
	CreatedAt  time.Time `json:"createdAt"`

	// Source overrides captured at submit time (v0.5): the executor resolves
	// the upstream in this order: SourceURL > credential URL > REGISTRY_URL
	// default > docker.io. Secrets are never serialized.
	SourceURL   string `json:"sourceUrl,omitempty"`
	SourceUser  string `json:"sourceUser,omitempty"`
	SourcePass  string `json:"-"`
	ProxyURL    string `json:"-"`
	FinalDigest string `json:"finalDigest,omitempty"`

	// Phases is the per-step detail the UI's expanded row renders
	// (manifest → config → blob:N → child-manifests). Mutated only under
	// the job mutex with copy-on-write (see setPhases), so snapshots
	// returned by View() stay stable.
	Phases []Phase `json:"phases,omitempty"`
}

// Job is one pull task. The mutex/cancelFn are private; handlers receive
// JobView (which is a pure value).
type Job struct {
	view JobView

	mu       sync.Mutex
	cancelFn context.CancelFunc
}

// IsTerminal reports whether the job no longer needs the executor's attention.
func (j *Job) IsTerminal() bool {
	switch j.view.State {
	case StateSucceeded, StateFailed, StateCancelled:
		return true
	}
	return false
}

// View returns a copy of the job's public fields.
func (j *Job) View() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := j.view
	return v
}

// MutexHeld runs fn while holding the job's mutex. Use this when the
// orchestrator needs to mutate several view fields atomically.
func (j *Job) MutexHeld(fn func(v *JobView)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	fn(&j.view)
}

// setPhases replaces v.Phases with a mutated copy (copy-on-write). Views
// handed out earlier keep their old snapshot, so a polling UI never sees a
// half-updated phase list. Call with the job mutex held.
func setPhases(v *JobView, fn func(phases []Phase)) {
	next := make([]Phase, len(v.Phases))
	copy(next, v.Phases)
	fn(next)
	v.Phases = next
}

// appendPhase adds p to the job's phase list and returns its index (for
// later updatePhase calls).
func appendPhase(j *Job, p Phase) int {
	idx := 0
	j.MutexHeld(func(v *JobView) {
		idx = len(v.Phases)
		next := make([]Phase, idx+1)
		copy(next, v.Phases)
		next[idx] = p
		v.Phases = next
	})
	return idx
}

// updatePhase mutates the phase at idx copy-on-write. An out-of-range idx
// is a no-op: phase bookkeeping must never crash a pull.
func updatePhase(j *Job, idx int, fn func(p *Phase)) {
	j.MutexHeld(func(v *JobView) {
		if idx < 0 || idx >= len(v.Phases) {
			return
		}
		setPhases(v, func(phases []Phase) { fn(&phases[idx]) })
	})
}

// Executor is the FIFO pull queue with a single concurrent worker.
//
// The queue holds a ring of the most recent N jobs (cfg.QueueSize). When a
// job moves to a terminal state, the oldest non-running entry is evicted if
// the ring is full — but the running job is never evicted.
type Executor struct {
	mu        sync.Mutex
	jobs      map[string]*Job // id → Job (full history within queue size)
	order     []string        // FIFO insertion order (capped at queueSize)
	queueSize int
	runCh     chan struct{} // signals "there's a queued job to run"

	runJob func(ctx context.Context, j *Job) error // injected by orchestrator

	// DB persists terminal-state snapshots so the UI's history survives
	// a restart. v0.7.17: nil-safe; when nil, executeOne skips recording
	// (no history, like pre-v0.7.12 behavior).
	DB *db.Db
}

// NewExecutor creates a queue of size queueSize. runJob is called serially;
// the executor guarantees at most one concurrent invocation.
func NewExecutor(queueSize int, runJob func(ctx context.Context, j *Job) error) *Executor {
	if queueSize <= 0 {
		queueSize = 50
	}
	return &Executor{
		jobs:      make(map[string]*Job),
		queueSize: queueSize,
		runCh:     make(chan struct{}, 1),
		runJob:    runJob,
	}
}

// NewJob is the submit-time description of a pull job. SourceURL/SourceUser/
// SourcePass/ProxyURL are optional overrides resolved by the executor.
type NewJob struct {
	SourceRef    string
	DestRepo     string
	DestTag      string
	CredentialID string
	ProxyID      string
	SourceURL    string
	SourceUser   string
	SourcePass   string
	ProxyURL     string
}

// Submit creates a new Job in state=queued and returns its view.
func (e *Executor) Submit(nj NewJob) JobView {
	j := &Job{
		view: JobView{
			ID:         newJobID(),
			SourceRef:  nj.SourceRef,
			DestRepo:   nj.DestRepo,
			DestTag:    nj.DestTag,
			Credential: nj.CredentialID,
			Proxy:      nj.ProxyID,
			SourceURL:  nj.SourceURL,
			SourceUser: nj.SourceUser,
			SourcePass: nj.SourcePass,
			ProxyURL:   nj.ProxyURL,
			State:      StateQueued,
			CreatedAt:  time.Now().UTC(),
			// Seed the manifest phase so a queued job already has
			// something to render in the expanded row.
			Phases: []Phase{{
				Name:    "manifest",
				Status:  PhasePending,
				Message: "Queued",
			}},
		},
	}

	e.mu.Lock()
	e.jobs[j.view.ID] = j
	e.order = append(e.order, j.view.ID)
	e.evictLocked()
	e.mu.Unlock()

	// non-blocking signal
	select {
	case e.runCh <- struct{}{}:
	default:
	}
	return j.View()
}

// Get returns a copy of the job's view by id, or zero JobView if unknown.
func (e *Executor) Get(id string) JobView {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, ok := e.jobs[id]
	if !ok {
		return JobView{}
	}
	return j.View()
}

// List returns JobViews in FIFO order (queued first, running next, terminal last).
func (e *Executor) List() []JobView {
	e.mu.Lock()
	ids := append([]string(nil), e.order...)
	e.mu.Unlock()

	out := make([]JobView, 0, len(ids))
	for _, id := range ids {
		e.mu.Lock()
		j, ok := e.jobs[id]
		e.mu.Unlock()
		if !ok {
			continue
		}
		out = append(out, j.View())
	}
	return out
}

// Cancel marks a job for cancellation. Idempotent; returns nil if the job
// is already in a terminal state.
func (e *Executor) Cancel(id string) error {
	e.mu.Lock()
	j, ok := e.jobs[id]
	e.mu.Unlock()
	if !ok {
		return ErrJobNotFound
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.IsTerminal() {
		return nil
	}
	if j.cancelFn != nil {
		j.cancelFn()
	}
	if j.view.State == StateQueued {
		j.view.State = StateCancelled
		j.view.EndedAt = time.Now().UTC()
	}
	return nil
}

// Delete removes a terminal job from the queue.
func (e *Executor) Delete(id string) error {
	e.mu.Lock()
	j, ok := e.jobs[id]
	if !ok {
		e.mu.Unlock()
		return ErrJobNotFound
	}
	if !j.IsTerminal() {
		e.mu.Unlock()
		return ErrJobNotTerminal
	}
	delete(e.jobs, id)
	for i, oid := range e.order {
		if oid == id {
			e.order = append(e.order[:i], e.order[i+1:]...)
			break
		}
	}
	e.mu.Unlock()
	return nil
}

// Run blocks until ctx is cancelled, executing queued jobs serially.
func (e *Executor) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		j := e.nextQueued(ctx)
		if j == nil {
			select {
			case <-ctx.Done():
				return
			case <-e.runCh:
				continue
			}
		}

		e.executeOne(ctx, j)
	}
}

// nextQueued returns the next queued job or nil if there isn't one.
func (e *Executor) nextQueued(ctx context.Context) *Job {
	for {
		e.mu.Lock()
		for _, id := range e.order {
			j, ok := e.jobs[id]
			if !ok {
				continue
			}
			if j.view.State == StateQueued {
				e.mu.Unlock()
				return j
			}
		}
		e.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil
		case <-e.runCh:
		}
	}
}

// executeOne runs one job to completion, transitioning its state.
func (e *Executor) executeOne(parent context.Context, j *Job) {
	jobCtx, cancel := context.WithCancel(parent)
	j.mu.Lock()
	jobID := j.view.ID
	j.view.State = StateRunning
	j.view.StartedAt = time.Now().UTC()
	j.cancelFn = cancel
	j.mu.Unlock()

	defer cancel()

	err := e.runJobGuarded(jobCtx, j, jobID)
	j.mu.Lock()
	j.view.EndedAt = time.Now().UTC()
	if jobCtx.Err() != nil {
		j.view.State = StateCancelled
	} else if err != nil {
		j.view.State = StateFailed
		j.view.Error = err.Error()
	} else {
		j.view.State = StateSucceeded
	}
	// A terminal job must not keep spinning phases: mark every straggler
	// failed so the expanded row explains where it stopped. (The UI has no
	// 'cancelled' phase status; the job-level state already says cancelled.)
	if j.view.State != StateSucceeded {
		msg := "Execution failed"
		if j.view.State == StateCancelled {
			msg = "Task cancelled"
		}
		setPhases(&j.view, func(phases []Phase) {
			for i := range phases {
				if phases[i].Status == PhaseRunning || phases[i].Status == PhasePending {
					phases[i].Status = PhaseFailed
					// Overwrite in-flight wording ("Queued",
					// "Fetching..."): it would read as a lie on a
					// terminal job.
					phases[i].Message = msg
				}
			}
		})
	}
	j.cancelFn = nil
	// Snapshot the terminal state under the lock; disk I/O runs after
	// unlock so a slow SQLite can't wedge concurrent readers.
	j.mu.Unlock()
	snap := j.View()

	// v0.7.17: record the *terminal* state to SQLite here (was previously
	// recorded in RunOne right after PutManifest, with vv.State still
	// "running"). That earlier write left a stale "running" row on disk,
	// which the UI happily showed as "拉取中" after a restart, exposing
	// a Cancel button for a job that was no longer in the executor —
	// hitting it produced "pull job not found" because Cancel() only
	// checks e.jobs. Recording after the flip keeps the on-disk state
	// honest.
	if e.DB != nil {
		recordCtx, cancelRec := context.WithTimeout(context.Background(), 5*time.Second)
		_ = e.DB.PullJobRecord(recordCtx, db.PullJobRow{
			ID:         snap.ID,
			SourceRef:  snap.SourceRef,
			DestRepo:   snap.DestRepo,
			DestTag:    snap.DestTag,
			State:      string(snap.State),
			BytesDone:  snap.BytesDone,
			BytesTotal: snap.BytesTotal,
			StartedAt:  snap.StartedAt,
			EndedAt:    snap.EndedAt,
			CreatedAt:  snap.CreatedAt,
			Error:      snap.Error,
		})
		// v0.7.18: also persist per-blob detail (j.view.Phases) so the
		// UI's expanded row keeps showing the per-layer digest / size
		// after a restart. Previously this lived only in memory and
		// every history row's expansion went blank. The phase list is
		// captured by snap (under the lock, before unlock), so we can
		// iterate it freely outside the lock now.
		if len(snap.Phases) > 0 {
			blobRows := make([]db.PullJobBlobRow, len(snap.Phases))
			for i, p := range snap.Phases {
				var size int64
				if p.TotalBytes != nil {
					size = *p.TotalBytes
				}
				blobRows[i] = db.PullJobBlobRow{
					JobID:   snap.ID,
					Index:   i,
					Name:    p.Name,
					Digest:  p.Digest,
					Size:    size,
					Status:  p.Status,
					Message: p.Message,
				}
			}
			_ = e.DB.PullJobBlobsRecord(recordCtx, blobRows)
		}
		cancelRec()
	}
}

// runJobGuarded calls runJob but converts a panic into an ordinary error.
//
// A single malformed job must never take the process down. An unrecovered
// panic inside the orchestrator (nil dereference, index out of range, ...)
// kills the HTTP server with every in-memory job it holds — the submitted
// job then looks like it "vanished", and the queue history is gone too.
// Degrading it to a failed job keeps the service (and the other jobs)
// alive and surfaces the defect in the UI row instead of the logs alone.
// (v0.5.16)
func (e *Executor) runJobGuarded(ctx context.Context, j *Job, jobID string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("pull job panicked",
				"job", jobID,
				"panic", fmt.Sprint(r),
				"stack", string(debug.Stack()))
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return e.runJob(ctx, j)
}

// evictLocked removes the oldest terminal job if the queue is over capacity.
// Called with e.mu held.
func (e *Executor) evictLocked() {
	for len(e.order) > e.queueSize {
		evicted := false
		for i, id := range e.order {
			j, ok := e.jobs[id]
			if !ok || j.view.State == StateRunning || j.view.State == StateQueued {
				continue
			}
			delete(e.jobs, id)
			e.order = append(e.order[:i], e.order[i+1:]...)
			evicted = true
			break
		}
		if !evicted {
			break
		}
	}
}

// Sentinel errors returned by Cancel/Delete.
var (
	ErrJobNotFound    = errors.New("pull job not found")
	ErrJobNotTerminal = errors.New("pull job is not in a terminal state")
)

// jobCounter disambiguates jobs created within the same clock tick.
//
// v0.5.10: this used to be a plain uint64 incremented with jobCounter++ in
// newJobID, which Submit calls BEFORE taking e.mu — an unsynchronised
// read-modify-write on a package-level variable, i.e. a real data race (the
// race detector flags it and concurrent submits can lose increments).
var jobCounter atomic.Uint64

// newJobID returns a short, time-ordered identifier.
func newJobID() string {
	return fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), jobCounter.Add(1))
}
