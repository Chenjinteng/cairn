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
// v0.7.23: cancel mechanism. Engine.Start wraps the run context with
// context.WithCancel (on top of context.WithoutCancel so client
// disconnects still don't kill a run); the cancel func is stored in
// e.cancels[taskID] so Engine.Cancel(taskID) can be invoked from any
// goroutine — typically the HTTP handler for POST /sync/{id}/cancel.
// The run goroutine's existing context.Canceled checks (runPull,
// pullRepo, pullFromSpecs, pullTag) handle the abort naturally; run
// status lands on 'failed' with an error mentioning cancellation. See
// Cancel for the failure-mode contract.
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
//   - context cancellation (either explicit Cancel() or — in the
//     detached-ctx world — basically never from upstream) aborts the
//     run immediately with one Info line ("pull aborted" / "push
//     aborted") instead of a WARN per repo, and the run lands in
//     'failed' — not 'partial' (SYNC-2).
type Engine struct {
	store *Store
	local storage.Storage
	vault *credentials.Vault // nil when the vault failed to open (no REGISTRY_CREDENTIAL_KEY)
	log   *slog.Logger

	perTaskMu sync.Mutex
	perTask   map[int64]*sync.Mutex

	// v0.7.23: per-task cancel funcs. Map[int64]cancel is safe because
	// Start holds the per-task lock before storing, so two concurrent
	// Start calls for the same task never both succeed (TryLock fails
	// the second). cancelMu guards the map; we never hold it across
	// cancel() — that call could trigger deep I/O aborts.
	cancelsMu sync.Mutex
	cancels   map[int64]context.CancelFunc
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
		cancels: make(map[int64]context.CancelFunc),
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
//
// v0.7.23: on top of the detach, we wrap with context.WithCancel so
// Engine.Cancel(taskID) can abort the run explicitly. The cancel func
// is stashed in e.cancels; execute's deferred cleanup deletes it on
// terminal-state write so the map never grows.
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
	// response. Then wrap with WithCancel so Cancel(taskID) can abort.
	// StartedAt / run.ID were persisted above; the goroutine writes the
	// terminal state with the same detached+cancelable context.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.cancelsMu.Lock()
	e.cancels[task.ID] = cancel
	e.cancelsMu.Unlock()

	go e.execute(runCtx, task, run, lock)

	return run, nil
}

// Cancel aborts the run currently in flight for taskID. Returns true if
// a run was in flight and the cancel was dispatched; false if no run
// is active for this task (handler maps to 400, not a no-op success,
// because the UI needs to know it clicked too late / wrong state).
//
// Cancellation is best-effort: it only signals the run goroutine's
// context. Network calls in flight (http.Client.Do on a 5-minute layer
// pull) won't be preempted; they finish or hit their own deadline first,
// then the next ctx.Err() check in pullTag / pullRepo / pullFromSpecs
// bails out. For a v0.7.21-era task pulling a 2 GB blob at 4 MB/s, the
// worst-case abort latency is one full layer download (~70 MB / ~17 s).
// The handler keeps the connection open and returns immediately — the
// run row stays 'running' for those few seconds, then flips to 'failed'
// with an error containing "context canceled". UI polls see the
// transition.
//
// Safe to call concurrently and from any goroutine.
func (e *Engine) Cancel(taskID int64) bool {
	e.cancelsMu.Lock()
	cancel, ok := e.cancels[taskID]
	if ok {
		delete(e.cancels, taskID)
	}
	e.cancelsMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// execute runs one iteration and always leaves the run row in a
// terminal state. It owns the per-task lock (released on return).
//
// v0.7.23: also clears e.cancels[taskID] on exit so Cancel() can't reach
// a stale cancel func for a task whose run already finished. Order of
// the two defers matters — unlock is on the outer scope; we put the
// cancels-delete inline so it runs after the terminal-state write
// but before the lock release.
//
// The terminal-state write uses a background context — separate from
// `ctx` (which is the runCtx and may already be canceled). Without
// this guard, Cancel() would leave the run row stuck on 'running'
// until the next process boot's MarkStaleRunsFailed sweep, because
// the SQL UPDATE on a canceled ctx returns ctx.Err() before it ever
// reaches SQLite. The transition should be visible immediately, not
// after a container restart.
func (e *Engine) execute(ctx context.Context, task SyncTask, run SyncRun, lock *sync.Mutex) {
	defer func() {
		e.cancelsMu.Lock()
		delete(e.cancels, task.ID)
		e.cancelsMu.Unlock()
		lock.Unlock()
	}()

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
	// (MarkStaleRunsFailed at boot). Use context.Background() rather
	// than ctx — see comment on execute for why.
	if err := e.store.UpdateRun(context.Background(), run); err != nil {
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

	// v0.7.21: tags_filter 旁路。某些上游(典型:匿名 TCR / Harbor)
	// 对 /v2/_catalog 返 401 / insufficient_scope,而 docker pull 走
	// manifest 直读并不需要 catalog —— 这条路径让用户用「固定若干
	// (repo, tag) 精确清单」绕过 catalog,直接 fetch manifest。
	// 空 / 全注释 / 全无效行 → 走回老的 ListRepositories 分支,旧任务行为不变。
	if specs := ParseTagsFilter(task.TagsFilter); len(specs) > 0 {
		// v0.7.22: pass longRepos so pullFromSpecs picks per-spec timeout.
		// Parsed here (not inside pullFromSpecs) so the work isn't repeated
		// on every spec — the list is shared across the whole run.
		longRepos := ParseLongTimeoutRepos(task.LongTimeoutRepos)
		return e.pullFromSpecs(ctx, task, username, password, run, specs, longRepos)
	}

	// Catalog path: one client for the whole run, default timeout.
	rc, err := newRemoteClient(task.RemoteURL, username, password, DefaultSyncTimeout)
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

// pullFromSpecs is the v0.7.21 catalog-bypass branch of runPull. Given a
// parsed TagSpec list, it pulls each (repo, tag) directly via GetManifest
// without ever asking the remote for /v2/_catalog or /v2/<repo>/tags/list.
//
// v0.7.22: each spec gets its own registry.Client with a per-spec
// timeout (Default vs Long, matched against longRepos). Per-spec clients
// are necessary because http.Client.Timeout is set at construction;
// reusing a single client with the wrong timeout would either kill
// small repos at 30 min or kill big repos at 5 min. The cost of one
// client per spec is negligible (TLS handshake amortised over the
// ~hundreds-of-MB blob transfer) and we CloseIdleConnections after each
// spec so the goroutine doesn't pin idle conns across the whole run.
//
// Per-tag failures don't abort the run — they're recorded as failed
// sync_run_items rows and counted into ReposFailed, matching the catalog
// path's contract (v0.6.16). A non-nil error return signals the whole run
// was aborted (context cancellation only — list-tags errors don't apply
// here).
//
// This is structurally the inner loop of pullRepo with the ListTags step
// removed; we don't reuse pullRepo because its name and return type both
// imply "operate on one repo", and feeding it a list of repos feels worse
// than a sibling function with its own doc.
func (e *Engine) pullFromSpecs(ctx context.Context, task SyncTask, username, password string, run *SyncRun, specs []TagSpec, longRepos []string) error {
	run.ReposTotal = len(specs)
	for _, s := range specs {
		// v0.7.22: per-spec timeout. Linear scan is fine — longRepos is
		// typically 1-5 entries; a map would cost more than it saves.
		timeout := DefaultSyncTimeout
		for _, r := range longRepos {
			if s.Repository == r {
				timeout = LongSyncTimeout
				break
			}
		}
		rc, err := newRemoteClient(task.RemoteURL, username, password, timeout)
		if err != nil {
			// No client = abort this spec, log, continue with the next.
			// A bad newRemoteClient here is a configuration bug (bad URL),
			// not a transient network error, so we don't retry.
			e.log.Warn("sync: build remote client failed (tags_filter)",
				"task_id", run.TaskID, "run_id", run.ID,
				"repo", s.Repository, "tag", s.Tag, "err", err)
			finishedAt := time.Now().UTC()
			e.recordRunItem(ctx, run.ID, s.Repository, s.Tag, finishedAt, finishedAt, 0, err)
			run.ReposFailed++
			continue
		}
		e.progress(ctx, run, s.Repository, s.Tag)
		startedAt := time.Now().UTC()
		bytesTotal, tagErr := e.pullTag(ctx, rc, s.Repository, s.Tag)
		finishedAt := time.Now().UTC()
		rc.HTTP().CloseIdleConnections()
		e.recordRunItem(ctx, run.ID, s.Repository, s.Tag, startedAt, finishedAt, bytesTotal, tagErr)
		if errors.Is(tagErr, context.Canceled) {
			return fmt.Errorf("run aborted: %w", tagErr)
		}
		if tagErr != nil {
			e.log.Warn("sync: pull tag failed (tags_filter)",
				"task_id", run.TaskID, "run_id", run.ID,
				"repo", s.Repository, "tag", s.Tag, "err", tagErr)
			run.ReposFailed++
			continue
		}
		run.ReposSynced++
	}
	return nil
}

// pullRepo iterates every tag in the remote repo and writes one
// sync_run_items row per (repo, tag) attempt. Per-tag failures no
// longer abort the whole repo — they're recorded as failed items and
// counted into ReposFailed by the caller (runPull). This is the v0.6.16
// behavior change: before, the first tag failure short-circuited the
// inner loop and the remaining tags in the same repo were never
// attempted, hiding partial-success repos behind a single error.
//
// The boolean return reports whether the repo succeeded (zero tag
// failures). runPull uses it for the repos_synced counter; the items
// table already holds the per-tag detail for the UI.
//
// A non-nil error return signals a *non-per-tag* failure (list-tags
// error, context cancellation) — these still abort the whole repo and
// propagate up.
func (e *Engine) pullRepo(ctx context.Context, rc *registry.Client, run *SyncRun, repoName string) error {
	tags, err := rc.ListTags(ctx, repoName)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}
	repoOK := true
	for _, tag := range tags {
		e.progress(ctx, run, repoName, tag)
		startedAt := time.Now().UTC()
		bytesTotal, tagErr := e.pullTag(ctx, rc, repoName, tag)
		finishedAt := time.Now().UTC()
		e.recordRunItem(ctx, run.ID, repoName, tag, startedAt, finishedAt, bytesTotal, tagErr)
		if errors.Is(tagErr, context.Canceled) {
			return fmt.Errorf("run aborted: %w", tagErr)
		}
		if tagErr != nil {
			e.log.Warn("sync: pull tag failed",
				"task_id", run.TaskID, "run_id", run.ID,
				"repo", repoName, "tag", tag, "err", tagErr)
			repoOK = false
			continue
		}
	}
	if !repoOK {
		return errors.New("one or more tags failed")
	}
	return nil
}

// pullTag returns the total bytes pulled for this tag (sum of layer
// sizes from the manifest) and any error. On error, bytesTotal still
// returns the declared manifest size so the failed item row in
// sync_run_items can show the user "X bytes / Y bytes" — useful for
// distinguishing "tiny image failed" from "10GB image failed" at a
// glance.
func (e *Engine) pullTag(ctx context.Context, rc *registry.Client, repoName, tag string) (int64, error) {
	mf, err := rc.GetManifest(ctx, repoName, tag)
	if err != nil {
		return 0, fmt.Errorf("get manifest: %w", err)
	}
	if isIndexMediaType(mf.MediaType) {
		return mf.Size, errors.New("multi-arch manifest index not supported in v0.6.0")
	}

	// Copy layers + config blob. Layers is populated by registry.Client
	// only for single-arch manifests (decodeManifestLayers); for indexes
	// we already returned above.
	for _, layer := range mf.Layers {
		if err := e.copyBlobToLocal(ctx, rc, repoName, layer.Digest); err != nil {
			return mf.Size, fmt.Errorf("layer %s: %w", layer.Digest, err)
		}
	}
	if mf.ConfigDigest != "" {
		if err := e.copyBlobToLocal(ctx, rc, repoName, mf.ConfigDigest); err != nil {
			return mf.Size, fmt.Errorf("config %s: %w", mf.ConfigDigest, err)
		}
	}

	if _, err := e.local.PutManifest(ctx, repoName, tag, mf.MediaType, mf.Raw); err != nil {
		return mf.Size, fmt.Errorf("put manifest: %w", err)
	}
	return mf.Size, nil
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

// pushRepo mirrors pullRepo on the push side: iterates local tags,
// writes one sync_run_items row per (repo, tag), and converts any
// per-tag failure into a failed item row + repoOK=false instead of
// aborting the whole repo. Same v0.6.16 contract as pullRepo.
func (e *Engine) pushRepo(ctx context.Context, w *Writer, run *SyncRun, repoName string) error {
	tags, err := e.local.Tags(ctx, repoName)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}
	repoOK := true
	for _, tag := range tags {
		e.progress(ctx, run, repoName, tag)
		startedAt := time.Now().UTC()
		bytesTotal, tagErr := e.pushTag(ctx, w, repoName, tag)
		finishedAt := time.Now().UTC()
		e.recordRunItem(ctx, run.ID, repoName, tag, startedAt, finishedAt, bytesTotal, tagErr)
		if errors.Is(tagErr, context.Canceled) {
			return fmt.Errorf("run aborted: %w", tagErr)
		}
		if tagErr != nil {
			e.log.Warn("sync: push tag failed",
				"task_id", run.TaskID, "run_id", run.ID,
				"repo", repoName, "tag", tag, "err", tagErr)
			repoOK = false
			continue
		}
	}
	if !repoOK {
		return errors.New("one or more tags failed")
	}
	return nil
}

// pushTag returns the total bytes pushed for this tag and any error.
// Same shape as pullTag — bytesTotal still returns the declared
// manifest size on failure so the failed item row shows "X / Y bytes".
func (e *Engine) pushTag(ctx context.Context, w *Writer, repoName, tag string) (int64, error) {
	mf, err := e.local.GetManifest(ctx, repoName, tag)
	if err != nil {
		return 0, fmt.Errorf("get manifest: %w", err)
	}
	if isIndexMediaType(mf.MediaType) {
		return 0, errors.New("multi-arch manifest index not supported in v0.6.0")
	}

	// storage.Manifest doesn't expose layer digests as a struct field, so
	// we parse the body (same shape as remote, since the remote sent us
	// an OCI/Docker manifest when the local image was originally pulled).
	layers, configDigest, err := parseManifestBlobs(mf.Body)
	if err != nil {
		return 0, fmt.Errorf("parse manifest body: %w", err)
	}

	// bytesTotal is the sum of layer sizes + config size — same
	// derivation as registry.Manifest.Size. We re-derive it from
	// mf.Body here because storage.Manifest doesn't expose the parsed
	// fields.
	var bytesTotal int64
	for _, l := range layers {
		if err := e.copyBlobToRemote(ctx, w, repoName, l); err != nil {
			return bytesTotal, fmt.Errorf("layer %s: %w", l, err)
		}
		bytesTotal += blobSizeFromManifest(mf.Body, l)
	}
	if configDigest != "" {
		if err := e.copyBlobToRemote(ctx, w, repoName, configDigest); err != nil {
			return bytesTotal, fmt.Errorf("config %s: %w", configDigest, err)
		}
		bytesTotal += blobSizeFromManifest(mf.Body, configDigest)
	}

	if _, err := w.PutManifest(ctx, repoName, tag, mf.MediaType, mf.Body); err != nil {
		return bytesTotal, fmt.Errorf("put manifest: %w", err)
	}
	return bytesTotal, nil
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

// DefaultSyncTimeout is the per-request body-read deadline used for the
// median sync spec (small/medium images, single layer a few hundred MB).
// 5 minutes is enough at ~4 MB/s public bandwidth for ~1 GB and short
// enough to surface hung layers (mid-stream connection idle-out, blob
// hash mismatch) before the next spec starts.
//
// v0.7.22: oversized repos (multi-GB / dozens of layers like
// bklite/bklite/vllm, bklite/bklite/server) blow past this — see
// LongSyncTimeout and engine.runPull's per-spec dispatch.
const DefaultSyncTimeout = 5 * time.Minute

// LongSyncTimeout is the deadline for spec.Repos in
// SyncTask.LongTimeoutRepos. 30 minutes covers a 7 GB blob at 4 MB/s
// (≈ 30 min) with headroom for the cumulative RTT per layer; raising
// further invites a slow-network troubleshooting nightmare where every
// spec waits 30 min before failing. Keep this paired with the
// LongTimeoutRepos opt-in rather than applying it globally.
const LongSyncTimeout = 30 * time.Minute

// newRemoteClient builds a registry.Client pointing at remoteURL with
// the destination's Basic-auth credentials stamped on every outbound
// request. Both fields empty = anonymous (skip auth header); the
// underlying registry.Client does the same.
//
// timeout is the per-request body-read deadline. Callers pick between
// DefaultSyncTimeout (median case) and LongSyncTimeout (whitelisted
// oversized repos, v0.7.22) per spec.
func newRemoteClient(remoteURL, username, password string, timeout time.Duration) (*registry.Client, error) {
	return registry.NewClient(registry.Config{
		BaseURL:  remoteURL,
		Username: username,
		Password: password,
		Timeout:  timeout,
	})
}
// recordRunItem persists one row to sync_run_items reflecting a single
// (repo, tag) attempt. Errors are logged + swallowed — a failed item
// row write must never abort a healthy sync (same contract as
// UpdateRunProgress).
//
// On success, state="succeeded", bytes_done=bytes_total.
// On failure, state="failed", bytes_done=0 (the engine doesn't track
// per-tag partial progress through a multi-layer copy), bytes_total
// is the declared manifest size when known so the UI can show
// "X / Y bytes" context.
//
// Cancellation is not a state we currently emit: by the time the
// engine knows the run is being cancelled (context.Canceled in the
// tag handler), it's already returning up the stack and never reaches
// this call. The "cancelled" enum value is reserved for future use.
func (e *Engine) recordRunItem(
	ctx context.Context, runID int64,
	repo, tag string,
	startedAt, finishedAt time.Time,
	bytesTotal int64, tagErr error,
) {
	state := "succeeded"
	errStr := ""
	var bytesDone int64 = bytesTotal
	if tagErr != nil {
		state = "failed"
		errStr = tagErr.Error()
		bytesDone = 0
	}
	item := SyncRunItem{
		RunID:      runID,
		Repository: repo,
		Tag:        tag,
		State:      state,
		Error:      errStr,
		BytesDone:  bytesDone,
		BytesTotal: bytesTotal,
		StartedAt:  startedAt,
		FinishedAt: &finishedAt,
	}
	if err := e.store.CreateRunItem(ctx, item); err != nil {
		e.log.Warn("sync: write run item failed",
			"run_id", runID, "repo", repo, "tag", tag, "err", err)
	}
}

// blobSizeFromManifest looks up a single blob's size in a parsed
// manifest body. Used by pushTag to compute bytes_total without
// re-fetching the manifest through the registry client. Returns 0
// when the digest isn't found in the body — non-fatal; the resulting
// sync_run_items row just shows bytes_total=0 for that layer.
func blobSizeFromManifest(body []byte, digest string) int64 {
	if digest == "" {
		return 0
	}
	var m struct {
		Layers []struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"layers"`
		Config struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return 0
	}
	if m.Config.Digest == digest {
		return m.Config.Size
	}
	for _, l := range m.Layers {
		if l.Digest == digest {
			return l.Size
		}
	}
	return 0
}
