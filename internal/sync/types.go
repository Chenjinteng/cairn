// Package sync implements cairn↔cairn registry mirroring. Tasks are stored
// in SQLite (see schema v5 in internal/db/db.go); the engine reads from one
// cairn and writes into the other according to the task's Direction.
//
// v0.6.0 surface: manual trigger only, bearer-token auth, pull + push per
// task, continue-on-error per repo. Cron scheduling, per-tag filter, exclude
// patterns, and live log streaming are deliberately deferred to v0.6.1+ per
// the 0.6.0 plan.
package sync

import (
	"errors"
	"strings"
	"time"
)

// Direction controls which way data flows in a sync task.
//
//	pull: this cairn (initiator) reads from `remote_url` and stores into
//	      its own local registry. Common case: "fill my cache from the
//	      authoritative cairn".
//	push: this cairn reads its local registry and writes into `remote_url`.
//	      Common case: "mirror my dev registry to the staging cairn".
type Direction string

const (
	DirectionPull Direction = "pull"
	DirectionPush Direction = "push"
)

// Valid reports whether d is one of the two known directions.
func (d Direction) Valid() bool {
	return d == DirectionPull || d == DirectionPush
}

// SyncRunStatus is the lifecycle state of one SyncRun.
//
//	running  — engine still iterating; FinishedAt is nil
//	success  — every repo synced, zero failures
//	partial  — at least one repo failed but the run finished iterating
//	failed   — run aborted before completion (remote unreachable, etc.)
type SyncRunStatus string

const (
	RunRunning SyncRunStatus = "running"
	RunSuccess SyncRunStatus = "success"
	RunFailed  SyncRunStatus = "failed"
	RunPartial SyncRunStatus = "partial"
)

// Valid reports whether s is one of the four known run statuses.
func (s SyncRunStatus) Valid() bool {
	return s == RunRunning || s == RunSuccess || s == RunFailed || s == RunPartial
}

// SyncTask is one configured registry sync (cairn↔cairn). Persisted in the
// sync_tasks table.
//
// RemoteToken is the bearer token this node presents on Authorization
// header when talking to RemoteURL. Stored plaintext in SQLite for v0.6.0;
// switching to credential-library references (see internal/config) is a
// v0.6.2+ follow-up. The `json:"-"` tag prevents accidental inclusion in
// API responses; use SyncTaskInput for the create/update form.
type SyncTask struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Direction   Direction `json:"direction"`
	RemoteURL   string    `json:"remoteUrl"`
	RemoteToken string    `json:"-"`
	Include     string    `json:"include"` // newline-separated glob patterns; "" matches all
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// SyncTaskInput is the JSON shape POST/PUT/PATCH /api/sync accepts. It
// exists separately from SyncTask only because the bearer token needs to
// be received on input — the json:"-" tag on SyncTask.RemoteToken would
// block that. handlers convert input → task via ToTask().
//
// RemoteToken is optional on update (empty == "don't change"); required on
// create (validated by SyncTask.Validate).
type SyncTaskInput struct {
	Name        string    `json:"name"`
	Direction   Direction `json:"direction"`
	RemoteURL   string    `json:"remoteUrl"`
	RemoteToken string    `json:"remoteToken"`
	Include     string    `json:"include"`
	Enabled     bool      `json:"enabled"`
}

// ToTask projects a SyncTaskInput into a SyncTask. The caller is expected
// to have already validated (or be about to validate) the resulting task.
func (in SyncTaskInput) ToTask() SyncTask {
	return SyncTask{
		Name:        in.Name,
		Direction:   in.Direction,
		RemoteURL:   in.RemoteURL,
		RemoteToken: in.RemoteToken,
		Include:     in.Include,
		Enabled:     in.Enabled,
	}
}

// Validate checks that t is acceptable for persistence. Same rules apply
// to both create and update; on update, callers may want to also check
// that t.ID != 0.
func (t *SyncTask) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return ErrTaskNameRequired
	}
	if !t.Direction.Valid() {
		return ErrInvalidDirection
	}
	if strings.TrimSpace(t.RemoteURL) == "" {
		return ErrRemoteURLRequired
	}
	if t.RemoteToken == "" {
		return ErrTokenRequired
	}
	return nil
}

// SyncRun is one execution of a SyncTask.
//
// Created in 'running' state when the engine picks the task up; flipped
// to 'success' / 'partial' / 'failed' when iteration completes or
// aborts. FinishedAt is nil while running.
//
// ReposTotal / ReposSynced / ReposFailed are the per-run summary
// counters the UI shows in the history table. Per-repo error detail is
// NOT persisted (would balloon SQLite); the engine logs them instead.
type SyncRun struct {
	ID          int64         `json:"id"`
	TaskID      int64         `json:"taskId"`
	StartedAt   time.Time     `json:"startedAt"`
	FinishedAt  *time.Time    `json:"finishedAt,omitempty"`
	Status      SyncRunStatus `json:"status"`
	ReposTotal  int           `json:"reposTotal"`
	ReposSynced int           `json:"reposSynced"`
	ReposFailed int           `json:"reposFailed"`
	Error       string        `json:"error,omitempty"`
}

// Sentinel errors used by Validate and the store layer. Handlers should
// match on these (with errors.Is) to return 400 vs 409 vs 500 correctly.
var (
	ErrInvalidDirection  = errors.New("sync: invalid direction (must be pull or push)")
	ErrInvalidStatus     = errors.New("sync: invalid run status")
	ErrTaskNameRequired  = errors.New("sync: task name is required")
	ErrRemoteURLRequired = errors.New("sync: remote URL is required")
	ErrTokenRequired     = errors.New("sync: remote token is required")
	ErrTaskNotFound      = errors.New("sync: task not found")
	ErrRunNotFound       = errors.New("sync: run not found")
	ErrTaskNameConflict  = errors.New("sync: task name already exists")
	ErrInvalidURL        = errors.New("sync: invalid remote URL (need scheme + host)")
)