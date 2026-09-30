package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Chenjinteng/cairn/internal/credentials"
	"github.com/Chenjinteng/cairn/internal/registry"
	"github.com/Chenjinteng/cairn/internal/storage"
)

// Engine executes sync tasks. One Engine per process; constructed in
// server.go from the live *db.Db handle + local storage.Storage.
//
// Concurrency model (v0.6.8): Start persists the 'running' run row and
// returns immediately; iteration continues on a background goroutine
// with a context detached from the triggering HTTP request, so neither
// the response nor a client disconnect/refresh can abort a run mid-way
// (SYNC-1 / SYNC-2).
//   - Per-task mutex, acquired with a non-blocking TryLock: a second
//     Start on a task with a run in flight fails fast with
//     ErrTaskRunning (handler → 409) instead of queueing behind the
//     first run (SYNC-4).
//   - Cross-task runs proceed in parallel.
//   - Within a single run, repos are processed sequentially. Each repo
//     does many small HTTP calls + blob copies; parallelism here would
//     mostly hit the remote's rate limit. Parallelism across repos is
//     a v0.6.2+ follow-up.
//
// Credentials (v0.6.8 / SYNC-3): when a task carries a
// RemoteCredentialID, the Basic-auth pair is resolved from the
// credential library at run time — rotating a password there takes
// effect on the next run without re-editing the task. A dangling
// reference fails the run with ErrCredentialNotFound; it never falls
// back to anonymous.
//
// Failure semantics:
//   - continue-on-error per repo: one bad repo doesn't abort the run.
//   - per-tag failures bubble up as per-repo failures (tags within a
//     repo are tightly sequenced — manifest references blobs that must
//     all arrive; partial manifests are useless).
//   - run-level failures (remote unreachable, list-repos denied,
//     credential resolution failed) abort the whole run with
//     status=failed and Error=...; repos_total is populated even if 0
//     so the UI shows "0 attempted".
//   - context cancellation aborts the run immediately with one Info
//     line ("pull aborted" / "push aborted") instead of a WARN per
//     repo, and the run lands in 'failed' — not 'partial' (SYNC-2).
type Engine struct {
	store *Store
	local storage.Storage
	vault *credentials.Vault // nil when the vault failed to open (no REGISTRY_CREDENTIAL_KEY)
	log   *slog.Logger

	perTaskMu sync.Mutex
	perTask   map[int64]*sync.Mutex
}

// NewEngine constructs an Engine. vault may be nil — tasks that
// reference the credential library then fail their runs with a clear
// error instead of panicking; inline/anon tasks are unaffected. log may
// be nil (defaults to slog.Default()).
func NewEngine(store *Store, local storage.Storage, vault *credentials.Vault, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		store:   store,
		local:   local,
		vault:   vault,
		log:     log,
		perTask: make(map[int64]*sync.Mutex),
	}
}

// Start validates the task, claims its per-task lock, persists a
// 'running' SyncRun row, and launches iteration on a background
// goroutine. It returns the running run as soon as the row is written —
// the HTTP request is NOT held open for the duration of the sync
// (SYNC-1: "run now" used to die on the browser's 10s timeout and leave
// a zombie running row behind).
//
// The returned error is START-level only:
//   - ErrTaskDisabled — task.Enabled is false (handler → 400).
//   - ErrInvalidDirection — unknown direction value (handler → 400).
//   - ErrTaskRunning — a run for this task is already in flight
//     (handler → 409; SYNC-4).
//   - anything wrapping a CreateRun store failure (handler → 500).
//
// Failures DURING iteration never surface here; the run row is updated
// to its terminal state (failed / partial / success) by the background
// goroutine, observable via ListRunsByTask and the task's lastRunStatus.
//
// The run context is deliberately detached (context.WithoutCancel): a
// client disconnect, a page refresh, or the response completing must
// not cancel the sync (SYNC-2 — the old synchronous design logged a
// WARN per repo with "context canceled" the moment the browser gave up).
func (e *Engine) Start(ctx context.Context, task SyncTask) (SyncRun, error) {
	if !task.Enabled {
		return SyncRun{}, ErrTaskDisabled
	}
	if !task.Direction.Valid() {
		return SyncRun{}, ErrInvalidDirection
	}

	lock := e.lockFor(task.ID)
	if !lock.TryLock() {
		return SyncRun{}, ErrTaskRunning
	}

	now := time.Now().UTC()
	run := SyncRun{
		TaskID:    task.ID,
		StartedAt: now,
		Status:    RunRunning,
	}
	if err := e.store.CreateRun(ctx, &run); err != nil {
		lock.Unlock()
		return SyncRun{}, fmt.Errorf("sync: create run row: %w", err)
	}

	// Detach from the request context: iteration must outlive the HTTP
	// response. StartedAt / run.ID were persisted above; the goroutine
	// writes the terminal state with the same detached context.
	go e.execute(context.WithoutCancel(ctx), task, run, lock)

	return run, nil
}

// execute runs one iteration and always leaves the run row in a
// terminal state. It owns the per-task lock (released on return).
func (e *Engine) execute(ctx context.Context, task SyncTask, run SyncRun, lock *sync.Mutex) {
	defer lock.Unlock()

	runErr := e.iterate(ctx, task, &run)

	finished := time.Now().UTC()
	run.FinishedAt = &finished
	switch {
	case runErr != nil:
		run.Status = RunFailed
		run.Error = runErr.Error()
	case run.ReposFailed > 0:
		run.Status = RunPartial
	default:
		run.Status = RunSuccess
	}

	// Best-effort terminal write: losing it only means the row stays
	// 'running' until the next process start sweeps it
	// (MarkStaleRunsFailed at boot).
	if err := e.store.UpdateRun(ctx, run); err != nil {
		e.log.Warn("sync: update run row failed",
			"run_id", run.ID, "task_id", task.ID, "err", err)
	}
}

// iterate runs the direction-specific iteration. A panic is converted
// into a run-level error (never re-panics — a buggy task must not take
// the process down, and the deferred lock release above still runs).
// progress stamps the current (repo, tag) into the run row so the UI can
// show "正在拉 bklite/cloud-ide:v1.2.3" while iteration is in flight
// (v0.6.9). Pass tag="" to mark "started this repo, no tag yet".
//
// Single-row UPDATE; cheap enough to fire hundreds of times per run.
// Errors are logged at WARN and swallowed — progress is a UI hint, never
// a correctness signal. A failed progress write must NEVER abort a
// healthy sync.
func (e *Engine) progress(ctx context.Context, run *SyncRun, repo, tag string) {
	if err := e.store.UpdateRunProgress(ctx, run.ID, repo, tag); err != nil {
		e.log.Warn("sync: write progress failed",
			"task_id", run.TaskID, "run_id", run.ID, "repo", repo, "tag", tag, "err", err)
	}
}

func (e *Engine) iterate(ctx context.Context, task SyncTask, run *SyncRun) (runErr error) {
	defer func() {
		if rec := recover(); rec != nil {
			e.log.Error("sync: run panicked",
				"task_id", task.ID, "run_id", run.ID, "panic", rec)
			runErr = fmt.Errorf("internal panic: %v", rec)
		}
	}()

	switch task.Direction {
	case DirectionPull:
		return e.runPull(ctx, task, run)
	case DirectionPush:
		return e.runPush(ctx, task, run)
	}
	return ErrInvalidDirection
}

func (e *Engine) lockFor(taskID int64) *sync.Mutex {
	e.perTaskMu.Lock()
	defer e.perTaskMu.Unlock()
	lock, ok := e.perTask[taskID]
	if !ok {
		lock = &sync.Mutex{}
		e.perTask[taskID] = lock
	}
	return lock
}

// resolveCredentials returns the Basic-auth pair to use against the
// remote (v0.6.8 / SYNC-3):
//   - RemoteCredentialID set → resolved from the credential library at
//     run time, so a rotated password takes effect without re-editing
//     the task.
//   - Reference empty → the legacy inline pair (both empty = anonymous;
//     Validate guarantees the pair is never half-filled).
//
// Every error here is run-level: a dangling reference fails the run
// loudly rather than silently downgrading to anonymous (which would
// surface as confusing 401s against the remote).
func (e *Engine) resolveCredentials(task SyncTask) (username, password string, err error) {
	ref := strings.TrimSpace(task.RemoteCredentialID)
	if ref == "" {
		return task.RemoteUsername, task.RemotePassword, nil
	}
	if e.vault == nil {
		return "", "", errors.New("credential library unavailable (REGISTRY_CREDENTIAL_KEY is not set)")
	}
	cred, err := e.vault.Get(ref)
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			return "", "", fmt.Errorf("%w (id=%s)", ErrCredentialNotFound, ref)
		}
		return "", "", fmt.Errorf("read credential %q: %w", ref, err)
	}
	return cred.Username, cred.Password, nil
}

// --- pull: read remote, write local ---------------------------------------

func (e *Engine) runPull(ctx context.Context, task SyncTask, run *SyncRun) error {
	username, password, err := e.resolveCredentials(task)
	if err != nil {
		return err
	}
	rc, err := newRemoteClient(task.RemoteURL, username, password)
	if err != nil {
		return fmt.Errorf("build remote client: %w", err)
	}

	repos, err := rc.ListRepositories(ctx)
	if err != nil {
		return fmt.Errorf("list remote repos: %w", err)
	}
	repos = NewFilter(task.Include).Apply(repos)
	run.ReposTotal = len(repos)

	for _, repoName := range repos {
		e.progress(ctx, run, repoName, "")
		if err := e.pullRepo(ctx, rc, run, repoName); err != nil {
			if errors.Is(err, context.Canceled) {
				// The run is being torn down as a whole; one Info line
				// instead of a WARN per remaining repo (SYNC-2).
				e.log.Info("sync: pull aborted",
					"task_id", task.ID, "repo", repoName, "err", err)
				return fmt.Errorf("run aborted: %w", err)
			}
			e.log.Warn("sync: pull repo failed",
				"task_id", task.ID, "repo", repoName, "err", err)
			run.ReposFailed++
			continue
		}
		run.ReposSynced++
	}
	return nil
}

func (e *Engine) pullRepo(ctx context.Context, rc *registry.Client, run *SyncRun, repoName string) error {
	tags, err := rc.ListTags(ctx, repoName)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}
	for _, tag := range tags {
		e.progress(ctx, run, repoName, tag)
		if err := e.pullTag(ctx, rc, repoName, tag); err != nil {
			return fmt.Errorf("tag %q: %w", tag, err)
		}
	}
	return nil
}

func (e *Engine) pullTag(ctx context.Context, rc *registry.Client, repoName, tag string) error {
	mf, err := rc.GetManifest(ctx, repoName, tag)
	if err != nil {
		return fmt.Errorf("get manifest: %w", err)
	}
	if isIndexMediaType(mf.MediaType) {
		return errors.New("multi-arch manifest index not supported in v0.6.0")
	}

	// Copy layers + config blob. Layers is populated by registry.Client
	// only for single-arch manifests (decodeManifestLayers); for indexes
	// we already returned above.
	for _, layer := range mf.Layers {
		if err := e.copyBlobToLocal(ctx, rc, repoName, layer.Digest); err != nil {
			return fmt.Errorf("layer %s: %w", layer.Digest, err)
		}
	}
	if mf.ConfigDigest != "" {
		if err := e.copyBlobToLocal(ctx, rc, repoName, mf.ConfigDigest); err != nil {
			return fmt.Errorf("config %s: %w", mf.ConfigDigest, err)
		}
	}

	if _, err := e.local.PutManifest(ctx, repoName, tag, mf.MediaType, mf.Raw); err != nil {
		return fmt.Errorf("put manifest: %w", err)
	}
	return nil
}

func (e *Engine) copyBlobToLocal(ctx context.Context, rc *registry.Client, repo, digest string) error {
	exists, err := e.local.BlobExists(ctx, repo, digest)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	body, size, err := rc.GetBlob(ctx, repo, digest)
	if err != nil {
		return fmt.Errorf("get blob: %w", err)
	}
	defer body.Close()
	return e.uploadBlobToLocal(ctx, repo, digest, body, size)
}

func (e *Engine) uploadBlobToLocal(ctx context.Context, repo, digest string, body io.Reader, blobSize int64) error {
	uuid, err := e.local.StartUpload(ctx, repo)
	if err != nil {
		return err
	}
	if _, err := e.local.PatchUpload(ctx, repo, uuid, -1, body); err != nil {
		// Best-effort cleanup — the upload session leaks bytes until GC
		// reclaims it (24h cutoff), but we don't want to leave them
		// around if we know the upload is bad.
		_ = e.local.CancelUpload(ctx, repo, uuid)
		return err
	}
	return e.local.PutUpload(ctx, repo, uuid, digest)
}

// --- push: read local, write remote ---------------------------------------

func (e *Engine) runPush(ctx context.Context, task SyncTask, run *SyncRun) error {
	username, password, err := e.resolveCredentials(task)
	if err != nil {
		return err
	}
	writer, err := NewWriter(task.RemoteURL, username, password)
	if err != nil {
		return fmt.Errorf("build remote writer: %w", err)
	}

	repos, err := e.local.Repositories(ctx)
	if err != nil {
		return fmt.Errorf("list local repos: %w", err)
	}
	repos = NewFilter(task.Include).Apply(repos)
	run.ReposTotal = len(repos)

	for _, repoName := range repos {
		e.progress(ctx, run, repoName, "")
		if err := e.pushRepo(ctx, writer, run, repoName); err != nil {
			if errors.Is(err, context.Canceled) {
				e.log.Info("sync: push aborted",
					"task_id", task.ID, "repo", repoName, "err", err)
				return fmt.Errorf("run aborted: %w", err)
			}
			e.log.Warn("sync: push repo failed",
				"task_id", task.ID, "repo", repoName, "err", err)
			run.ReposFailed++
			continue
		}
		run.ReposSynced++
	}
	return nil
}

func (e *Engine) pushRepo(ctx context.Context, w *Writer, run *SyncRun, repoName string) error {
	tags, err := e.local.Tags(ctx, repoName)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}
	for _, tag := range tags {
		e.progress(ctx, run, repoName, tag)
		if err := e.pushTag(ctx, w, repoName, tag); err != nil {
			return fmt.Errorf("tag %q: %w", tag, err)
		}
	}
	return nil
}

func (e *Engine) pushTag(ctx context.Context, w *Writer, repoName, tag string) error {
	mf, err := e.local.GetManifest(ctx, repoName, tag)
	if err != nil {
		return fmt.Errorf("get manifest: %w", err)
	}
	if isIndexMediaType(mf.MediaType) {
		return errors.New("multi-arch manifest index not supported in v0.6.0")
	}

	// storage.Manifest doesn't expose layer digests as a struct field, so
	// we parse the body (same shape as remote, since the remote sent us
	// an OCI/Docker manifest when the local image was originally pulled).
	layers, configDigest, err := parseManifestBlobs(mf.Body)
	if err != nil {
		return fmt.Errorf("parse manifest body: %w", err)
	}

	for _, l := range layers {
		if err := e.copyBlobToRemote(ctx, w, repoName, l); err != nil {
			return fmt.Errorf("layer %s: %w", l, err)
		}
	}
	if configDigest != "" {
		if err := e.copyBlobToRemote(ctx, w, repoName, configDigest); err != nil {
			return fmt.Errorf("config %s: %w", configDigest, err)
		}
	}

	if _, err := w.PutManifest(ctx, repoName, tag, mf.MediaType, mf.Body); err != nil {
		return fmt.Errorf("put manifest: %w", err)
	}
	return nil
}

func (e *Engine) copyBlobToRemote(ctx context.Context, w *Writer, repo, digest string) error {
	body, size, err := e.local.GetBlob(ctx, repo, digest)
	if err != nil {
		return fmt.Errorf("get blob: %w", err)
	}
	defer body.Close()
	return w.EnsureBlob(ctx, repo, digest, body, size)
}

// --- shared helpers -------------------------------------------------------

// parseManifestBlobs extracts the layer + config digests from a single-arch
// manifest body. Index manifests (manifest.list / image.index) are rejected
// by isIndexMediaType upstream; if one slips through, the JSON unmarshal
// just won't find layers[] and we'll return an empty list (which then
// produces a manifest with no blobs — visible in the run error).
func parseManifestBlobs(body []byte) (layers []string, configDigest string, err error) {
	var m struct {
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, "", err
	}
	for _, l := range m.Layers {
		if l.Digest != "" {
			layers = append(layers, l.Digest)
		}
	}
	return layers, m.Config.Digest, nil
}

// isIndexMediaType reports whether mediaType denotes a multi-arch index.
// Both OCI ("image.index") and Docker ("manifest.list") forms are caught.
func isIndexMediaType(mediaType string) bool {
	return strings.Contains(mediaType, "manifest.list") ||
		strings.Contains(mediaType, "image.index")
}

// --- Basic-auth remote client construction --------------------------------
//
// v0.6.1: pivoted from bearer to Basic to match cairn's /v2/* Basic
// middleware. registry.Config already has Username + Password fields that
// flow through to http.Request.SetBasicAuth on every outbound call, so
// we don't need a transport wrapper — just hand the creds to NewClient.
//
// The pair may come from the credential library (v0.6.8 / SYNC-3) —
// resolution happens in resolveCredentials before this point.

// newRemoteClient builds a registry.Client pointing at remoteURL with
// the destination's Basic-auth credentials stamped on every outbound
// request. Both fields empty = anonymous (skip auth header); the
// underlying registry.Client does the same.
func newRemoteClient(remoteURL, username, password string) (*registry.Client, error) {
	return registry.NewClient(registry.Config{
		BaseURL:  remoteURL,
		Username: username,
		Password: password,
		Timeout:  5 * time.Minute,
	})
}