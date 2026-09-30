package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Chenjinteng/cairn/internal/sync"
)

// SyncHandlers wires the registry-sync REST endpoints onto the API
// surface. One instance per process; constructed in server.go with the
// live *sync.Store + *sync.Engine. RegisterRoutes mounts the routes
// at /api/sync relative to the /api group; the parent callsite in
// handlers_extra.go is responsible for invoking it (so this file stays
// the single source of truth for sync API shape).
type SyncHandlers struct {
	Store  *sync.Store
	Engine *sync.Engine
	Log    *slog.Logger
}

// RegisterRoutes mounts the /sync endpoints on the supplied router.
// Paths here are RELATIVE to /api (the parent group in api.go).
//
// Routes:
//
//	GET    /sync               — list tasks
//	POST   /sync               — create task
//	GET    /sync/{id}          — fetch one task
//	PATCH  /sync/{id}          — update task (token field is optional;
//	                             empty == keep current token)
//	DELETE /sync/{id}          — delete task (cascades runs)
//	POST   /sync/{id}/run      — trigger a sync run; blocks until done
//	GET    /sync/{id}/runs     — list recent runs (newest first,
//	                             default limit 50)
func (s *SyncHandlers) RegisterRoutes(r chi.Router) {
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

// ListTasks — GET /api/sync
//
// Returns every task, newest first. The bearer token field is
// stripped from the JSON response via the json:"-" tag on
// SyncTask.RemoteToken (see internal/sync/types.go).
func (s *SyncHandlers) ListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.Store.ListTasks(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if tasks == nil {
		tasks = []sync.SyncTask{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

// CreateTask — POST /api/sync
//
// Body shape: SyncTaskInput (json with all fields). The bearer token
// is required here (the operator pasting it from the destination
// cairn's settings page). On success, returns the created task with
// assigned ID + timestamps.
//
// 201 Created          — task persisted
// 400 BAD_REQUEST      — body invalid JSON or fails SyncTask.Validate
// 409 CONFLICT         — name already exists
func (s *SyncHandlers) CreateTask(w http.ResponseWriter, r *http.Request) {
	var in sync.SyncTaskInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	t := in.ToTask()
	if err := t.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.Store.CreateTask(r.Context(), &t); err != nil {
		if errors.Is(err, sync.ErrTaskNameConflict) {
			writeError(w, r, http.StatusConflict, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// GetTask — GET /api/sync/{id}
func (s *SyncHandlers) GetTask(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	t, err := s.Store.GetTask(r.Context(), id)
	if err != nil {
		if errors.Is(err, sync.ErrTaskNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// UpdateTask — PATCH /api/sync/{id}
//
// Body shape: SyncTaskInput. Token field is OPTIONAL — empty in the
// request means "keep the stored token". This avoids forcing operators
// to retype the token on every other-field edit (which would also be
// a UX problem: the UI never has it on screen in the first place).
//
// 200 OK               — task updated
// 400 BAD_REQUEST      — JSON invalid or Validate() fails
// 404 NOT_FOUND        — id doesn't exist
// 409 CONFLICT         — new name collides with another task
func (s *SyncHandlers) UpdateTask(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var in sync.SyncTaskInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}

	existing, err := s.Store.GetTask(r.Context(), id)
	if err != nil {
		if errors.Is(err, sync.ErrTaskNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	// Apply patch. Password: keep stored value unless input explicitly
	// supplies a new one (lets UI edit other fields via a partial PUT
	// that doesn't carry the secret). Username is always overwritten —
	// it's not sensitive, the UI has it on screen already, and treating
	// "blank username == keep current" would mean a username-less input
	// silently leaves a stale value.
	if in.RemotePassword != "" {
		existing.RemotePassword = in.RemotePassword
	}
	existing.Name = in.Name
	existing.Direction = in.Direction
	existing.RemoteURL = in.RemoteURL
	existing.RemoteUsername = in.RemoteUsername
	existing.Include = in.Include
	existing.Enabled = in.Enabled

	if err := existing.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.Store.UpdateTask(r.Context(), &existing); err != nil {
		if errors.Is(err, sync.ErrTaskNameConflict) {
			writeError(w, r, http.StatusConflict, err)
			return
		}
		if errors.Is(err, sync.ErrTaskNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

// DeleteTask — DELETE /api/sync/{id}
//
// CASCADE removes all sync_runs for the task (FK in schema v5).
// Returns 204 No Content on success.
func (s *SyncHandlers) DeleteTask(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteTask(r.Context(), id); err != nil {
		if errors.Is(err, sync.ErrTaskNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunTask — POST /api/sync/{id}/run
//
// Blocks until done — v0.6.0 has no async/scheduler, and UI flow is
// "click Run → spinner → see result inline". v0.6.1+ adds cron, at which
// point this should switch to 202 + a poll URL.
//
// HTTP status reflects the run outcome:
//   200 OK               — run.Status ∈ {success, partial}
//   502 BAD_GATEWAY      — run.Status == failed (upstream unreachable,
//                         auth denied, etc. — the remote was the
//                         problem, not us)
//
// Per-repo failures don't bubble up as a non-2xx; they're reflected
// in the response body's run.Status / run.ReposFailed.
func (s *SyncHandlers) RunTask(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	task, err := s.Store.GetTask(r.Context(), id)
	if err != nil {
		if errors.Is(err, sync.ErrTaskNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	run, _ := s.Engine.Run(r.Context(), task)

	switch run.Status {
	case sync.RunSuccess, sync.RunPartial:
		writeJSON(w, http.StatusOK, run)
	case sync.RunFailed:
		writeJSON(w, http.StatusBadGateway, run)
	default:
		// RunRunning shouldn't reach here (Engine.Run always sets a
		// terminal status), but fall through to 200 rather than 500
		// for a status we don't recognize.
		writeJSON(w, http.StatusOK, run)
	}
}

// ListRuns — GET /api/sync/{id}/runs?limit=50
//
// Returns the most recent runs for a task, newest first. limit
// defaults to 50; pass 0 or omit to get all runs (UI uses 50 to
// keep the history table small — older history is a retention concern
// for v0.6.2+).
func (s *SyncHandlers) ListRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	runs, err := s.Store.ListRunsByTask(r.Context(), id, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if runs == nil {
		runs = []sync.SyncRun{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// parseID parses the {id} path parameter as int64. Writes a 400 and
// returns ok=false if the id is missing or not a valid integer.
func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, r, http.StatusBadRequest, errors.New("id must be a positive integer"))
		return 0, false
	}
	return id, true
}