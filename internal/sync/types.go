// Package sync implements cairn↔cairn registry mirroring. Tasks are stored
// in SQLite (see schema v5–v7 in internal/db/db.go); the engine reads from
// one cairn and writes into the other according to the task's Direction.
//
// v0.6.1 surface: manual trigger only, Basic auth (user/pass against
// cairn's /v2/* Basic middleware), pull + push per task, continue-on-error
// per repo. Cron scheduling, per-tag filter, exclude patterns, and live
// log streaming are deliberately deferred to v0.6.1+ per the 0.6.0 plan.
//
// v0.6.8: runs execute asynchronously (Engine.Start returns immediately
// with a 'running' row; POST /api/sync/{id}/run answers 202). Auth may
// reference the encrypted credential library instead of inline fields.
package sync

import (
	"errors"
	"fmt"
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
// v0.6.1 (post-0.6.0 hotfix): auth model pivoted from bearer to Basic to
// match cairn's own registry auth (cfg.RegistryUsername/Password → /v2/*
// Basic middleware). cairn has no bearer-token concept; the previous
// design's "paste a bearer token" had no source on the receiving side.
//
// v0.6.4: relaxed to support anonymous remotes. When both
// RemoteUsername and RemotePassword are empty, the engine omits the
// Authorization header entirely (cairn's requireBasicAuth middleware
// falls through when cfg.RegistryUsername is unset, so the request
// succeeds without credentials). Mixed (one empty, one set) is
// rejected as a UI typo — Validate returns ErrCredentialIncomplete.
//
// RemoteUsername + RemotePassword are the destination's basic-auth
// credentials, sent on every outbound request as
// `Authorization: Basic base64(user:pass)` (when both non-empty).
// Username alone is not sensitive (visible in API responses); the
// password is hidden via json:"-" — same shape as SyncTaskInput for
// password edits (empty on PATCH == "don't change" UNLESS both empty,
// which sets the task to anonymous).
//
// v0.6.8 (SYNC-3): auth may instead reference the encrypted credential
// library (internal/credentials) via RemoteCredentialID. The referenced
// pair is resolved at run time, so rotating a password in the credential
// library takes effect on the next run without re-editing tasks. Inline
// plaintext fields remain supported for back-compat with v0.6.x tasks.
type SyncTask struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	Direction          Direction `json:"direction"`
	RemoteURL          string    `json:"remoteUrl"`
	RemoteCredentialID string    `json:"remoteCredentialId,omitempty"`
	RemoteUsername     string    `json:"remoteUsername"`
	RemotePassword     string    `json:"-"`
	Include            string    `json:"include"` // newline-separated glob patterns; "" matches all
	TagsFilter         string    `json:"tagsFilter,omitempty"` // v0.7.21: newline-separated `repo:tag` specs; "" = catalog path
	LongTimeoutRepos   string    `json:"longTimeoutRepos,omitempty"` // v0.7.22: comma-separated repo names that get 30min client timeout; "" = all repos use 5min
	Enabled            bool      `json:"enabled"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`

	// LastRunStatus is NOT a sync_tasks column. The store fills it from
	// a correlated subquery over sync_runs (latest run per task) so the
	// UI can tell "a run is still in flight" from the list response
	// alone — this is what keeps the run button disabled across page
	// refreshes (v0.6.8 SYNC-4). Empty when the task has never run.
	LastRunStatus SyncRunStatus `json:"lastRunStatus,omitempty"`
	// LastRunCurrentRepo / LastRunCurrentTag mirror the same subquery for
	// the v0.6.9 "engine is currently working on this (repo, tag)" UI
	// indicator. Same source row as LastRunStatus — empty when the task
	// has never run OR when the engine isn't iterating yet (e.g. just
	// starting repo enumeration, repo known but tag not yet).
	LastRunCurrentRepo string `json:"lastRunCurrentRepo,omitempty"`
	LastRunCurrentTag  string `json:"lastRunCurrentTag,omitempty"`
}

// SyncTaskInput is the JSON shape POST/PUT/PATCH /api/sync accepts. It
// mirrors SyncTask but explicitly includes the password field (hidden on
// SyncTask via json:"-"). handlers convert input → task via ToTask().
//
// v0.6.8 (SYNC-3) three-state merge, applied by the UPDATE handler
// before validation:
//   - RemoteCredentialID != "" → reference mode; inline fields cleared.
//   - RemoteCredentialID == "" with RemoteUsername != "" → inline mode;
//     an empty RemotePassword means "keep the stored password".
//   - everything empty → anonymous mode; reference and inline fields
//     all cleared.
//
// On create there is nothing to merge: the payload is taken as-is and
// validated. RemotePassword must be empty whenever RemoteCredentialID
// is set (Validate enforces this via ErrCredentialConflict).
type SyncTaskInput struct {
	Name               string    `json:"name"`
	Direction          Direction `json:"direction"`
	RemoteURL          string    `json:"remoteUrl"`
	RemoteCredentialID string    `json:"remoteCredentialId"`
	RemoteUsername     string    `json:"remoteUsername"`
	RemotePassword     string    `json:"remotePassword"`
	Include            string    `json:"include"`
	TagsFilter         string    `json:"tagsFilter"` // v0.7.21: newline-separated `repo:tag`; empty = catalog path
	LongTimeoutRepos   string    `json:"longTimeoutRepos"` // v0.7.22: comma-separated repo names; empty = use 5min default
	Enabled            bool      `json:"enabled"`
}

// ToTask projects a SyncTaskInput into a SyncTask. The caller is expected
// to have already validated (or be about to validate) the resulting task.
func (in SyncTaskInput) ToTask() SyncTask {
	return SyncTask{
		Name:               in.Name,
		Direction:          in.Direction,
		RemoteURL:          in.RemoteURL,
		RemoteCredentialID: in.RemoteCredentialID,
		RemoteUsername:     in.RemoteUsername,
		RemotePassword:     in.RemotePassword,
		Include:            in.Include,
		TagsFilter:         in.TagsFilter,
		LongTimeoutRepos:   in.LongTimeoutRepos,
		Enabled:            in.Enabled,
	}
}

// Validate checks that t is acceptable for persistence. Same rules apply
// to both create and update; on update, callers may want to also check
// that t.ID != 0.
//
// Credential rule (v0.6.8 — three mutually exclusive states):
//   - RemoteCredentialID set → reference mode. Inline username/password
//     must both be empty (the engine resolves the pair from the
//     credential library at run time).
//   - Reference empty, both inline fields empty → anonymous remote.
//     Used when the destination cairn has no Registry Username/Password
//     configured (requireBasicAuth middleware falls through). The engine
//     simply omits the Authorization header; the receiving server
//     doesn't gate on it.
//   - Reference empty, both inline fields set → Basic auth with the
//     supplied inline pair (legacy mode, still supported).
//   - Anything mixed (reference + inline, or username without password)
//     is almost certainly a UI bug; reject so the operator sees
//     ErrCredentialConflict / ErrCredentialIncomplete rather than a
//     confusing 401 from the remote.
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
	u := strings.TrimSpace(t.RemoteUsername)
	p := t.RemotePassword
	ref := strings.TrimSpace(t.RemoteCredentialID)
	if ref != "" {
		if u != "" || p != "" {
			return ErrCredentialConflict
		}
		return nil
	}
	if (u == "") != (p == "") {
		return ErrCredentialIncomplete
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
	// CurrentRepo / CurrentTag identify which (repo, tag) the engine was
	// on when the run terminated. Stays set on failed runs so the UI can
	// say "failed at this image" without log-diving. Always empty on a
	// brand-new run, and stays set after SyncRunUpdate (the engine
	// intentionally does NOT clear them in UpdateRun — see SyncRunUpdate
	// comment). v0.6.9.
	CurrentRepo string `json:"currentRepo,omitempty"`
	CurrentTag  string `json:"currentTag,omitempty"`
}

// SyncRunItem is one (repo, tag) attempt inside a SyncRun (v0.6.16).
//
// Persisted as one row per pullTag / pushTag call. Granularity is
// per-(repo, tag) — symmetric to pull_jobs on the pull side — because
// that's what the engine actually iterates: one blob-copy attempt per
// tag. The UI history modal renders these as expandable rows under
// each run.
//
// State matches the CHECK constraint on sync_run_items.state:
//   - "succeeded" — copy completed (BytesDone == BytesTotal expected)
//   - "failed"    — copy errored; Error carries the engine's text
//   - "cancelled" — reserved for future per-tag cancellation; not
//                  currently emitted because the engine returns from
//                  pullRepo/pushRepo before writing per-tag rows when
//                  the run is being torn down
//
// BytesDone / BytesTotal mirror pull_jobs: meaningful for both pull
// (where bytes_done tracks progress through blob stream) and push
// (where bytes_done tracks what made it to the remote before any
// failure). 0/0 on cancellation is fine.
type SyncRunItem struct {
	ID         int64      `json:"id"`
	RunID      int64      `json:"runId"`
	Repository string     `json:"repository"`
	Tag        string     `json:"tag"`
	State      string     `json:"state"`
	Error      string     `json:"error,omitempty"`
	BytesDone  int64      `json:"bytesDone"`
	BytesTotal int64      `json:"bytesTotal"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// SyncRunItemSummary is the pre-aggregated count breakdown for one
// run's items, used by the UI to render a summary row without walking
// the full items list. Total = Succeeded + Failed + Cancelled.
type SyncRunItemSummary struct {
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// Sentinel errors used by Validate and the store layer. Handlers should
// match on these (with errors.Is) to return 400 vs 409 vs 500 correctly.
var (
	ErrInvalidDirection     = errors.New("sync: invalid direction (must be pull or push)")
	ErrInvalidStatus        = errors.New("sync: invalid run status")
	ErrTaskNameRequired     = errors.New("sync: task name is required")
	ErrRemoteURLRequired    = errors.New("sync: remote URL is required")
	ErrCredentialIncomplete = errors.New("sync: remote username and password must both be set, or both empty (anonymous)")
	ErrCredentialConflict   = errors.New("sync: a task cannot reference a credential and carry inline credentials at the same time")
	ErrCredentialNotFound   = errors.New("sync: referenced credential not found")
	ErrTaskNotFound         = errors.New("sync: task not found")
	ErrRunNotFound          = errors.New("sync: run not found")
	ErrTaskNameConflict     = errors.New("sync: task name already exists")
	ErrInvalidURL           = errors.New("sync: invalid remote URL (need scheme + host)")
	ErrInvalidCron          = errors.New("sync: invalid cron expression (need 5 fields: min hour dom mon dow)")
	ErrInvalidTimezone      = errors.New("sync: invalid IANA timezone (time.LoadLocation failed)")
)

// ErrTaskDisabled is returned by Engine.Start when the task's Enabled
// flag is false. Handlers translate it to 400.
var ErrTaskDisabled = errors.New("sync: task is disabled")

// ErrTaskRunning is returned by Engine.Start when the task already has a
// run in flight (per-task lock held). Handlers translate it to 409 so a
// second "run now" click — including one after a page refresh — fails
// loudly instead of racing the first run (SYNC-4).
var ErrTaskRunning = errors.New("sync: task is already running")

// Schedule is one cron firing rule attached to a SyncTask. v0.6.11.
//
// The scheduler loop (internal/sync/scheduler.go) scans the table for
// enabled rows whose NextRunAt <= now() every 30s and calls Engine.Start
// for the task. Engine.Start is the same entry the HTTP handler uses,
// so every guarantee v0.6.8 SYNC-1/4 added (TryLock, detached ctx,
// recover) applies identically to scheduled runs.
//
// CronExpr is a 5-field standard expression (minute hour dom month dow)
// with vanilla *, -, /, , syntax — no Quartz extensions (?, L, W, #).
//
// v0.6.31: Timezone field is kept on the wire + DB for backward compat
// but is no longer used by the cron evaluator. NextAfter (cron.go)
// evaluates in Asia/Shanghai unconditionally. Handlers + store force
// this field to "Asia/Shanghai" on every write; the UI no longer asks
// the user for it. The schema column stays TEXT NOT NULL DEFAULT '' —
// no migration needed; old rows may carry other values but they're
// ignored at evaluation time.
type Schedule struct {
	ID         int64      `json:"id"`
	TaskID     int64      `json:"taskId"`
	CronExpr   string     `json:"cronExpr"`
	Timezone   string     `json:"-"` // v0.6.31: deprecated; always Asia/Shanghai. Hidden from API.
	Enabled    bool       `json:"enabled"`
	NextRunAt  time.Time  `json:"nextRunAt"`
	LastRunAt  *time.Time `json:"lastRunAt,omitempty"`
	LastRunID  *int64     `json:"lastRunId,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// Validate parses CronExpr and populates NextRunAt so the caller can
// persist a sane future timestamp in one transaction. Empty CronExpr
// returns ErrInvalidCron; invalid syntax bubbles up the same error
// wrapped with field-level context.
//
// v0.6.31: Timezone validation removed — the field is no longer
// consulted at evaluation time (NextAfter is hardcoded Asia/Shanghai).
// Old rows carrying other timezone values still pass Validate; their
// stored value is ignored when the scheduler fires them.
//
// "now" is injected so tests can pin time. Pass time.Now().UTC() in
// production. NextRunAt is returned in UTC regardless of Timezone —
// the scheduler compares against the DB clock which is also UTC.
func (s *Schedule) Validate(now time.Time) error {
	if _, err := ParseCron(s.CronExpr); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCron, err)
	}
	next, err := NextAfter(s.CronExpr, now)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCron, err)
	}
	s.NextRunAt = next.UTC()
	return nil
}
