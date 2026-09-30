package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Chenjinteng/cairn/internal/registry"
	"github.com/Chenjinteng/cairn/internal/storage"
)

// Engine executes sync tasks. One Engine per process; constructed in
// server.go from the live *db.Db handle + local storage.Storage.
//
// Concurrency model:
//   - Per-task mutex (sync.Mutex per task.ID): the same task can't be
//     running twice simultaneously; cross-task runs proceed in parallel.
//   - Within a single run, repos are processed sequentially. Each repo
//     does many small HTTP calls + blob copies; parallelism here would
//     mostly hit the remote's rate limit. Parallelism across repos is
//     a v0.6.2+ follow-up.
//
// Failure semantics:
//   - continue-on-error per repo: one bad repo doesn't abort the run.
//   - per-tag failures bubble up as per-repo failures (tags within a
//     repo are tightly sequenced — manifest references blobs that must
//     all arrive; partial manifests are useless).
//   - run-level failures (remote unreachable, list-repos denied) abort
//     the whole run with status=failed and Error=...; repos_total is
//     populated even if 0 so the UI shows "0 attempted".
type Engine struct {
	store *Store
	local storage.Storage
	log   *slog.Logger

	perTaskMu sync.Mutex
	perTask   map[int64]*sync.Mutex
}

// NewEngine constructs an Engine. log may be nil (defaults to slog.Default()).
func NewEngine(store *Store, local storage.Storage, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		store:   store,
		local:   local,
		log:     log,
		perTask: make(map[int64]*sync.Mutex),
	}
}

// Run executes one task synchronously and returns its terminal SyncRun.
// The run is persisted (sync_runs row created at start, updated at end)
// so callers see it via ListRunsByTask even if they discard the return.
//
// Concurrent Run calls on the same task.ID serialize. The second caller
// blocks until the first finishes, then begins its own iteration.
//
// Errors returned here are RUN-LEVEL — they mean the engine could not
// even start iterating (e.g. remote URL invalid, list-repos denied).
// Per-repo failures do NOT bubble up; they're recorded as repos_failed
// in the SyncRun and the returned error is nil in that case.
func (e *Engine) Run(ctx context.Context, task SyncTask) (SyncRun, error) {
	if !task.Enabled {
		return SyncRun{}, errors.New("sync: task is disabled")
	}
	if !task.Direction.Valid() {
		return SyncRun{}, ErrInvalidDirection
	}

	lock := e.lockFor(task.ID)
	lock.Lock()
	defer lock.Unlock()

	now := time.Now().UTC()
	run := SyncRun{
		TaskID:    task.ID,
		StartedAt: now,
		Status:    RunRunning,
	}
	if err := e.store.CreateRun(ctx, &run); err != nil {
		return SyncRun{}, fmt.Errorf("sync: create run row: %w", err)
	}

	var runErr error
	switch task.Direction {
	case DirectionPull:
		runErr = e.runPull(ctx, task, &run)
	case DirectionPush:
		runErr = e.runPush(ctx, task, &run)
	}

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

	if uerr := e.store.UpdateRun(ctx, run); uerr != nil {
		// Don't lose the original run error; just log the bookkeeping failure.
		e.log.Warn("sync: update run row failed",
			"run_id", run.ID, "task_id", task.ID, "err", uerr)
	}

	return run, runErr
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

// --- pull: read remote, write local ---------------------------------------

func (e *Engine) runPull(ctx context.Context, task SyncTask, run *SyncRun) error {
	rc, err := newRemoteClient(task.RemoteURL, task.RemoteToken)
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
		if err := e.pullRepo(ctx, rc, repoName); err != nil {
			e.log.Warn("sync: pull repo failed",
				"task_id", task.ID, "repo", repoName, "err", err)
			run.ReposFailed++
			continue
		}
		run.ReposSynced++
	}
	return nil
}

func (e *Engine) pullRepo(ctx context.Context, rc *registry.Client, repoName string) error {
	tags, err := rc.ListTags(ctx, repoName)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}
	for _, tag := range tags {
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
	writer, err := NewWriter(task.RemoteURL, task.RemoteToken)
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
		if err := e.pushRepo(ctx, writer, repoName); err != nil {
			e.log.Warn("sync: push repo failed",
				"task_id", task.ID, "repo", repoName, "err", err)
			run.ReposFailed++
			continue
		}
		run.ReposSynced++
	}
	return nil
}

func (e *Engine) pushRepo(ctx context.Context, w *Writer, repoName string) error {
	tags, err := e.local.Tags(ctx, repoName)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}
	for _, tag := range tags {
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

// --- pre-issued bearer transport wrapper ----------------------------------
//
// The upstream registry.Client has its own bearer flow: on first 401 it
// fetches a token from the realm in WWW-Authenticate, caches it, and
// retries. That works fine for interactive use but burns an extra round
// trip per session — and in the sync case we already have the token
// (it came out of sync_tasks.remote_token). Wrapping the http.Transport
// is the smallest change to upstream that lets us present the header
// unconditionally without forking Client or modifying internal/registry.

// newRemoteClient builds a registry.Client pointing at remoteURL with a
// pre-issued bearer token stamped on every outbound request. An empty
// token means anonymous; the wrapping is skipped in that case so we
// don't shadow any auth the underlying transport might add.
func newRemoteClient(remoteURL, token string) (*registry.Client, error) {
	rc, err := registry.NewClient(registry.Config{
		BaseURL: remoteURL,
		Timeout: 5 * time.Minute,
	})
	if err != nil {
		return nil, err
	}
	if token != "" {
		rc.HTTP().Transport = &bearerTransport{
			token: token,
			base:  rc.HTTP().Transport,
		}
	}
	return rc, nil
}

// bearerTransport stamps `Authorization: Bearer <token>` on every
// outbound request, then delegates to the wrapped transport (the one
// registry.NewClient configured). We clone the request before mutating
// headers so the caller's request isn't polluted for retry paths —
// http.Client mutates headers in place, and the 401-retry path inside
// Client would otherwise see our bearer on the retry too (which is
// actually fine, but cloning is the convention here).
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}