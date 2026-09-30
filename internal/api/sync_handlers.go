package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	syncstd "sync"
	"time"

	"github.com/go-chi/chi/v5"

	syncpkg "github.com/Chenjinteng/cairn/internal/sync"
)

// SyncAPI groups the handlers and dependencies for the /api/sync/* surface.
// Pulled out of ExtraHandlers so the v0.2/v0.3 routes stay focused — same
// pattern the pull / credentials / proxies handlers use.
type SyncAPI struct {
	Engine *syncpkg.Engine
	Store  *syncpkg.Store

	// runMu serialises task triggers within a process. Two POSTs to
	// /api/sync/{id}/run at the same moment must not both start the
	// same task — the second one should see "already running" and
	// either wait or 409. Phase 3's cron will share the same gate.
	runMu   syncstd.Mutex
	running map[string]bool // task_id → currently in flight
}

// NewSyncAPI wires an API surface around an engine + store pair. Returns
// nil when engine or store is missing so handlers can fall through to
// 503 (matches the pull / credentials disabled-mode contract).
func NewSyncAPI(engine *syncpkg.Engine, store *syncpkg.Store) *SyncAPI {
	if engine == nil || store == nil {
		return nil
	}
	return &SyncAPI{
		Engine:  engine,
		Store:   store,
		running: map[string]bool{},
	}
}

// RegisterRoutes mounts the v0.6.0 sync endpoints under /api/sync.
// All routes go through the chi router that the parent /api group owns.
func (s *SyncAPI) RegisterRoutes(r chi.Router) {
	if s == nil {
		// Disabled mode: register a single 503 handler so the UI gets a
		// clear "feature not configured" rather than 404.
		r.Route("/sync", func(r chi.Router) {
			r.Get("/*", notConfiguredSync)
			r.Post("/*", notConfiguredSync)
			r.Delete("/*", notConfiguredSync)
			r.Patch("/*", notConfiguredSync)
		})
		return
	}
	r.Route("/sync", func(r chi.Router) {
		r.Get("/", s.ListTasks)
		r.Post("/", s.CreateTask)
		r.Get("/{id}", s.GetTask)
		r.Patch("/{id}", s.UpdateTask)
		r.Delete("/{id}", s.DeleteTask)
		r.Post("/{id}/run", s.RunTask)
		r.Get("/{id}/runs", s.ListRuns)
	})
}

// notConfiguredSync is the catch-all for the disabled-mode mount above.
// Returns 503 with a stable code so the UI can branch on it.
func notConfiguredSync(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable,
		errors.New("sync module not initialised (engine or store missing)"))
}

// --- handlers ----------------------------------------------------------

// ListTasks returns all configured sync tasks. Newest first would be
// nicer, but the task volume is tiny (low double digits), so the DB's
// ORDER BY created_at ASC matches the pull queue ordering.
func (s *SyncAPI) ListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.Store.ListTasks(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

// CreateTaskReq is the POST /api/sync body. Direction defaults to "pull"
// when omitted (the only value Phase 1 implements); CredentialID is the
// vault reference (Phase 1 dec3:打通).
type CreateTaskReq struct {
	Name             string   `json:"name"`
	Direction        string   `json:"direction"`
	SourceURL        string   `json:"sourceUrl"`
	SourceRepoPrefix string   `json:"sourceRepoPrefix"`
	TargetRepoPrefix string   `json:"targetRepoPrefix"`
	DenyList         []string `json:"denyList"`
	CredentialID     string   `json:"credentialId"`
	Schedule         string   `json:"schedule"`
	Enabled          *bool    `json:"enabled,omitempty"`
}

// CreateTask persists a new task. name must be unique; SourceURL must
// be a non-empty URL (we don't dial until /run). Direction is locked to
// "pull" until Phase 2 lands — push is rejected with a 400 so the UI
// gets a clear error rather than silent failure at /run time.
func (s *SyncAPI) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req CreateTaskReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	if strings.TrimSpace(req.SourceURL) == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("sourceUrl is required"))
		return
	}
	if req.Direction == "" {
		req.Direction = string(syncpkg.DirectionPull)
	}
	if req.Direction != string(syncpkg.DirectionPull) {
		writeError(w, r, http.StatusBadRequest,
			fmt.Errorf("direction %q not implemented (only %q in Phase 1)", req.Direction, syncpkg.DirectionPull))
		return
	}
	if req.CredentialID != "" && s.Engine.ResolveCredential == nil {
		writeError(w, r, http.StatusBadRequest,
			errors.New("credentialId requires a credential vault to be configured"))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	t := syncpkg.Task{
		ID:               newSyncID(),
		Name:             strings.TrimSpace(req.Name),
		Direction:        syncpkg.Direction(req.Direction),
		SourceURL:        strings.TrimSpace(req.SourceURL),
		SourceRepoPrefix: strings.TrimSpace(req.SourceRepoPrefix),
		TargetRepoPrefix: strings.TrimSpace(req.TargetRepoPrefix),
		DenyList:         req.DenyList,
		CredentialID:     strings.TrimSpace(req.CredentialID),
		Schedule:         strings.TrimSpace(req.Schedule),
		Enabled:          enabled,
	}
	created, err := s.Store.CreateTask(r.Context(), t)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// GetTask returns one task by id. 404 when missing.
func (s *SyncAPI) GetTask(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	t, err := s.Store.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if t.ID == "" {
		writeError(w, r, http.StatusNotFound, errors.New("task not found"))
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// UpdateTaskReq is the PATCH body. All fields optional; missing fields
// keep their existing value (we read-modify-write to make partial
// updates work). Direction is locked to "pull" the same way as create.
type UpdateTaskReq struct {
	Name             *string   `json:"name,omitempty"`
	Direction        *string   `json:"direction,omitempty"`
	SourceURL        *string   `json:"sourceUrl,omitempty"`
	SourceRepoPrefix *string   `json:"sourceRepoPrefix,omitempty"`
	TargetRepoPrefix *string   `json:"targetRepoPrefix,omitempty"`
	DenyList         *[]string `json:"denyList,omitempty"`
	CredentialID     *string   `json:"credentialId,omitempty"`
	Schedule         *string   `json:"schedule,omitempty"`
	Enabled          *bool     `json:"enabled,omitempty"`
}

func (s *SyncAPI) UpdateTask(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	var patch UpdateTaskReq
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	t, err := s.Store.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if t.ID == "" {
		writeError(w, r, http.StatusNotFound, errors.New("task not found"))
		return
	}
	if patch.Name != nil {
		t.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Direction != nil {
		if *patch.Direction != string(syncpkg.DirectionPull) {
			writeError(w, r, http.StatusBadRequest,
				fmt.Errorf("direction %q not implemented (only %q in Phase 1)", *patch.Direction, syncpkg.DirectionPull))
			return
		}
		t.Direction = syncpkg.Direction(*patch.Direction)
	}
	if patch.SourceURL != nil {
		t.SourceURL = strings.TrimSpace(*patch.SourceURL)
	}
	if patch.SourceRepoPrefix != nil {
		t.SourceRepoPrefix = strings.TrimSpace(*patch.SourceRepoPrefix)
	}
	if patch.TargetRepoPrefix != nil {
		t.TargetRepoPrefix = strings.TrimSpace(*patch.TargetRepoPrefix)
	}
	if patch.DenyList != nil {
		t.DenyList = *patch.DenyList
	}
	if patch.CredentialID != nil {
		t.CredentialID = strings.TrimSpace(*patch.CredentialID)
		if t.CredentialID != "" && s.Engine.ResolveCredential == nil {
			writeError(w, r, http.StatusBadRequest,
				errors.New("credentialId requires a credential vault to be configured"))
			return
		}
	}
	if patch.Schedule != nil {
		t.Schedule = strings.TrimSpace(*patch.Schedule)
	}
	if patch.Enabled != nil {
		t.Enabled = *patch.Enabled
	}
	updated, err := s.Store.UpdateTask(r.Context(), t)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if updated.ID == "" {
		writeError(w, r, http.StatusNotFound, errors.New("task vanished during update"))
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *SyncAPI) DeleteTask(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	n, err := s.Store.DeleteTask(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if n == 0 {
		writeError(w, r, http.StatusNotFound, errors.New("task not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunTask triggers an immediate run of the task. Phase 1 is synchronous:
// the HTTP call blocks until the run finishes (or fails). UI shows a
// "running" state by virtue of the call not returning. Phase 3 will keep
// the synchronous semantics (the request still blocks) so the UI can
// display the run result inline.
func (s *SyncAPI) RunTask(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")

	t, err := s.Store.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if t.ID == "" {
		writeError(w, r, http.StatusNotFound, errors.New("task not found"))
		return
	}
	if !t.Enabled {
		writeError(w, r, http.StatusConflict, errors.New("task is disabled (toggle enabled=true to run)"))
		return
	}

	// Single-flight: refuse a second concurrent run for the same task.
	// The cron scheduler in Phase 3 will share this gate.
	s.runMu.Lock()
	if s.running[t.ID] {
		s.runMu.Unlock()
		writeError(w, r, http.StatusConflict, errors.New("task is already running"))
		return
	}
	s.running[t.ID] = true
	s.runMu.Unlock()
	defer func() {
		s.runMu.Lock()
		delete(s.running, t.ID)
		s.runMu.Unlock()
	}()

	// Pre-create the run row so /api/sync/{id}/runs shows it from the
	// moment the call returns. Engine.Run will update counts/state.
	run := syncpkg.Run{
		ID:        newRunID(),
		TaskID:    t.ID,
		StartedAt: nowUTC(),
	}
	if err := s.Store.RecordRunStart(r.Context(), run); err != nil {
		writeError(w, r, http.StatusInternalServerError,
			fmt.Errorf("record run start: %w", err))
		return
	}

	final, runErr := s.Engine.Run(r.Context(), t)
	final.ID = run.ID
	final.TaskID = t.ID
	if final.StartedAt.IsZero() {
		final.StartedAt = run.StartedAt
	}
	if runErr != nil {
		if final.State == syncpkg.RunStateSuccess {
			final.State = syncpkg.RunStateFailed
		}
		if final.Error == "" {
			final.Error = runErr.Error()
		}
	}
	if final.FinishedAt.IsZero() {
		final.FinishedAt = nowUTC()
	}
	if err := s.Store.RecordRunFinish(r.Context(), final); err != nil {
		writeError(w, r, http.StatusInternalServerError,
			fmt.Errorf("record run finish: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, final)
}

// ListRuns returns the most recent runs for a task, newest first.
// limit <= 0 means "all" — the UI passes a bounded limit to keep the
// payload small. Returns an empty array when the task is unknown so
// the UI's list view doesn't have to special-case 404 vs empty.
func (s *SyncAPI) ListRuns(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	t, err := s.Store.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if t.ID == "" {
		writeError(w, r, http.StatusNotFound, errors.New("task not found"))
		return
	}
	// Limit parsing: pull-style. Empty / 0 / negative → all.
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		// ignore parse errors — caller gets "all"
		fmt.Sscanf(v, "%d", &limit)
	}
	runs, err := s.Store.ListRuns(r.Context(), id, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// newSyncID returns "st-" + 12 hex chars. Used as task primary key.
// 48 bits of entropy is plenty: sync task count is in the tens, not
// millions, and the id never leaves the cairn process.
func newSyncID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "st-" + hex.EncodeToString(b[:])
}

// newRunID is the run primary key. Same shape as syncpkg.Engine.newRunID
// but lives here so the handler can create the row BEFORE handing off
// to Engine.Run (which would otherwise generate its own id).
func newRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "sr-" + hex.EncodeToString(b[:])
}

// nowUTC is a tiny indirection so tests can fake the time.
var nowUTC = func() time.Time { return time.Now().UTC() }