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
	"sync"
	"time"
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
	runCh     chan struct{}   // signals "there's a queued job to run"

	runJob func(ctx context.Context, j *Job) error // injected by orchestrator
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

// Submit creates a new Job in state=queued and returns its view.
func (e *Executor) Submit(sourceRef, destRepo, destTag, credentialID, proxyID string) JobView {
	j := &Job{
		view: JobView{
			ID:         newJobID(),
			SourceRef:  sourceRef,
			DestRepo:   destRepo,
			DestTag:    destTag,
			Credential: credentialID,
			Proxy:      proxyID,
			State:      StateQueued,
			CreatedAt:  time.Now().UTC(),
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
	j.view.State = StateRunning
	j.view.StartedAt = time.Now().UTC()
	j.cancelFn = cancel
	j.mu.Unlock()

	defer cancel()

	err := e.runJob(jobCtx, j)
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
	j.cancelFn = nil
	j.mu.Unlock()
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

// newJobID returns a short, time-ordered identifier.
var jobCounter uint64

func newJobID() string {
	jobCounter++
	return fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), jobCounter)
}