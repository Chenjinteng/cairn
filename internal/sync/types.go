// Package sync implements the registry-to-registry sync engine for cairn
// (v0.6.0, a.k.a. "regsync 内建"). The module reads the catalog of a
// source registry, applies the deny-list and prefix filters, and writes
// the selected repositories to a destination registry through the
// standard V2 distribution API.
//
// Phase 1 scope (per docs/ROADMAP.md):
//
//   - Direction = pull only (remote → local).
//   - Manual trigger via POST /api/sync/{id}/run. Phase 3 adds the
//     cron-based scheduler.
//
// Future phases will add:
//
//   - Direction = push (local → remote), with engine.Abstract(src, dst)
//     generalising the copy path.
//   - scheduler.go: hand-written 5-field cron parser + ticker.
//
// Boundaries:
//
//   - The source half of a copy talks V2 through internal/registry.Client.
//   - The destination half talks to a local cairn-managed registry
//     through internal/storage.Storage.
//   - The credential vault (internal/credentials.Vault) is the single
//     source of truth for source-registry credentials; we never persist
//     secrets here.
package sync

import "time"

// Direction is the direction of one sync task. v0.6.0 Phase 1 only
// implements pull; push lands in Phase 2.
type Direction string

const (
	// DirectionPull copies repositories from a remote source registry
	// into the local cairn-managed registry.
	DirectionPull Direction = "pull"
	// DirectionPush copies repositories from the local cairn-managed
	// registry to a remote destination registry. Phase 2.
	DirectionPush Direction = "push"
)

// RunState is the terminal state of one sync run.
type RunState string

const (
	RunStateSuccess RunState = "success"
	RunStateFailed  RunState = "failed"
	RunStateSkipped RunState = "skipped"
)

// Task is the API-side view of one configured sync job. The DB row mirrors
// this but uses RFC3339 strings and JSON-encoded deny_list — Engine and
// Store convert on the way in/out so consumers never see the wire form.
type Task struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Direction        Direction `json:"direction"`
	SourceURL        string    `json:"sourceUrl"`
	SourceRepoPrefix string    `json:"sourceRepoPrefix"`
	TargetRepoPrefix string    `json:"targetRepoPrefix"`
	DenyList         []string  `json:"denyList"`
	CredentialID     string    `json:"credentialId"`
	Schedule         string    `json:"schedule"`
	Enabled          bool      `json:"enabled"`
	LastRunAt        string    `json:"lastRunAt"`
	LastRunStatus    string    `json:"lastRunStatus"`
	LastRunError     string    `json:"lastRunError"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// Run is one execution of one task. Counts are rolled up across all
// repos/tags the engine touched during this run; per-image detail is
// deliberately not persisted (matches pull_jobs history contract).
type Run struct {
	ID              string    `json:"id"`
	TaskID          string    `json:"taskId"`
	State           RunState  `json:"state"`
	StartedAt       time.Time `json:"startedAt"`
	FinishedAt      time.Time `json:"finishedAt"`
	ManifestsCopied int64     `json:"manifestsCopied"`
	BlobsCopied     int64     `json:"blobsCopied"`
	BlobsSkipped    int64     `json:"blobsSkipped"`
	BytesTotal      int64     `json:"bytesTotal"`
	Error           string    `json:"error"`
}

// Counts is the running tally returned by Engine.Run. UI surfaces
// these in the run history page; persistence happens via Store.
type Counts struct {
	ManifestsCopied int64
	BlobsCopied     int64
	BlobsSkipped    int64
	BytesTotal      int64
}

// IsZero reports whether no work was done. Used by the engine to
// decide whether a run was meaningful (success with 0 copies still
// counts — it means "nothing to sync, all up to date").
func (c Counts) IsZero() bool {
	return c.ManifestsCopied == 0 && c.BlobsCopied == 0 && c.BlobsSkipped == 0 && c.BytesTotal == 0
}