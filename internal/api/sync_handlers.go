package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Chenjinteng/cairn/internal/credentials"
	"github.com/Chenjinteng/cairn/internal/sync"
)

// SyncHandlers wires the registry-sync REST endpoints onto the API
// surface. One instance per process; constructed in server.go with the
// live *sync.Store + *sync.Engine + the credential vault. RegisterRoutes
// mounts the routes at /api/sync relative to the /api group; the parent
// callsite in handlers_extra.go is responsible for invoking it (so this
// file stays the single source of truth for sync API shape).
//
// Vault may be nil when REGISTRY_CREDENTIAL_KEY is unset (the vault is
// optional at boot). Endpoints that must resolve a credential reference
// then fail with 503 instead of pretending the reference is usable.
type SyncHandlers struct {
	Store  *sync.Store
	Engine *sync.Engine
	Vault  *credentials.Vault
	Log    *slog.Logger
}

// RegisterRoutes mounts the /sync endpoints on the supplied router.
// Paths here are RELATIVE to /api (the parent group in api.go).
//
// Routes:
//
//	GET    /sync                  — list tasks
//	POST   /sync                  — create task
//	GET    /sync/{id}             — fetch one task
//	PATCH  /sync/{id}             — update task (three-state credential
//	                                merge; see UpdateTask)
//	DELETE /sync/{id}             — delete task (cascades runs)
//	POST   /sync/{id}/run         — start a run; 202 + the running run,
//	                                409 while another run is in flight
//	GET    /sync/{id}/runs        — list recent runs (newest first,
//	                                default limit 50)
//	GET    /sync/{id}/runs/{rid}/items?limit=&offset=
//	                              — paginated (repo, tag) attempts inside
//	                                one run (v0.6.16). Used by the
//	                                history modal's expandable rows.
//	POST   /sync/test             — probe remote reachability + auth posture;
//	                                powers the form's "测试连接" button.
//	                                Does NOT require a saved task.
func (s *SyncHandlers) RegisterRoutes(r chi.Router) {
	r.Route("/sync", func(r chi.Router) {
		r.Get("/", s.ListTasks)
		r.Post("/", s.CreateTask)
		r.Get("/{id}", s.GetTask)
		r.Patch("/{id}", s.UpdateTask)
		r.Delete("/{id}", s.DeleteTask)
		r.Post("/{id}/run", s.RunTask)
		r.Get("/{id}/runs", s.ListRuns)
		r.Get("/{id}/runs/{rid}/items", s.ListRunItems)
		r.Post("/test", s.TestConnection)
		r.Route("/{id}/schedules", func(r chi.Router) {
			r.Get("/", s.ListSchedules)
			r.Post("/", s.CreateSchedule)
			r.Patch("/{sid}", s.UpdateSchedule)
			r.Delete("/{sid}", s.DeleteSchedule)
		})
	})
}

// ListTasks — GET /api/sync
//
// Returns every task, newest first. Secrets never leave the process:
// SyncTask marshals RemotePassword as json:"-" (see internal/sync/types.go).
//
// Each row also carries lastRunStatus — filled by the store from the newest
// sync_runs row via a correlated subquery — so the UI can keep the run
// button disabled across page refreshes while a run is still in flight
// (v0.6.8 SYNC-4).
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
// Body shape: SyncTaskInput. Auth is one of three mutually exclusive
// shapes, enforced by SyncTask.Validate: a credential reference
// (remoteCredentialId), inline username+password, or anonymous.
//
// 201 Created          — task persisted
// 400 BAD_REQUEST      — body invalid JSON or fails SyncTask.Validate
// 409 CONFLICT         — name already exists
//
// A dangling credential reference is NOT rejected here: references are
// resolved at run time (engine.resolveCredentials), so a task can be saved
// ahead of its credential and the failure surfaces on the run that needed
// it.
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
// Body shape: SyncTaskInput. The credential fields are merged as a
// three-state switch BEFORE validation (v0.6.8 SYNC-3):
//
//	remoteCredentialId != ""       → reference mode; inline fields cleared
//	username or password supplied  → inline mode; a blank password keeps the
//	                                 stored one (the UI never has it on
//	                                 screen), a blank username is rejected
//	                                 by Validate if no password is stored
//	everything blank               → anonymous mode; all auth cleared
//
// The merge keeps pre-0.6.8 clients working unchanged — they only ever send
// inline fields and already rely on "blank password == keep" — while giving
// the new UI one field to flip between modes.
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

	switch {
	case strings.TrimSpace(in.RemoteCredentialID) != "":
		existing.RemoteCredentialID = strings.TrimSpace(in.RemoteCredentialID)
		existing.RemoteUsername = ""
		existing.RemotePassword = ""
	case in.RemoteUsername != "" || in.RemotePassword != "":
		existing.RemoteCredentialID = ""
		existing.RemoteUsername = in.RemoteUsername
		if in.RemotePassword != "" {
			existing.RemotePassword = in.RemotePassword
		}
	default:
		existing.RemoteCredentialID = ""
		existing.RemoteUsername = ""
		existing.RemotePassword = ""
	}

	existing.Name = in.Name
	existing.Direction = in.Direction
	existing.RemoteURL = in.RemoteURL
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
//
// v0.6.12 (MA-5): used to return 204 No Content, which the frontend's
// request() helper could not parse — it always JSON-parses the body, and
// JSON.parse("") throws SyntaxError that surfaces as "删除失败：服务返回了非
// JSON 响应 (HTTP 204)". The deletion was actually happening but the UI
// was telling the user it failed. Now we return 200 + a JSON envelope
// like every other DELETE in /api/* (Handlers.DeleteRepository,
// DeleteManifestByDigest, etc.) so the frontend's envelope parser
// succeeds and the success branch runs (including the missing-list-refresh
// behaviour previously suppressed by the bogus failure path).
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
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

// RunTask — POST /api/sync/{id}/run
//
// Starts a run and returns immediately (202 Accepted) with the freshly
// created run row, status "running". Execution continues on the engine's
// own goroutine over a detached context, so the run is no longer tied to
// the HTTP request's lifetime. That is the v0.6.8 SYNC-1 fix: the UI's 10s
// client-side budget used to cancel the run mid-flight (the "运行失败：
// 请求超时（10 秒）已中止" toast) and leave a zombie 'running' row behind.
// Observe the outcome by polling GET /api/sync (lastRunStatus) or
// GET /api/sync/{id}/runs.
//
//	202 ACCEPTED         — run started; body is the running SyncRun
//	400 BAD_REQUEST      — task disabled, or unknown direction
//	404 NOT_FOUND        — id doesn't exist
//	409 CONFLICT         — a run for this task is already in flight
//	500 INTERNAL         — couldn't book the run (DB error)
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

	run, err := s.Engine.Start(r.Context(), task)
	if err != nil {
		switch {
		case errors.Is(err, sync.ErrTaskRunning):
			writeError(w, r, http.StatusConflict, err)
		case errors.Is(err, sync.ErrTaskDisabled), errors.Is(err, sync.ErrInvalidDirection):
			writeError(w, r, http.StatusBadRequest, err)
		default:
			if s.Log != nil {
				s.Log.Error("sync: start run failed", "task_id", id, "err", err)
			}
			writeError(w, r, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// ListRuns — GET /api/sync/{id}/runs?limit=50
//
// Returns the most recent runs for a task, newest first. limit
// defaults to 50; pass 0 or omit to get all runs (UI uses 50 to
// keep the history table small — older history is a retention concern
// for v0.6.2+).
//
// v0.6.12 (MA-6): adds the same task-exists guard ListSchedules already has
// (see the comment on that function for the rationale). Without it,
// "GET /api/sync/999/runs" for a typo'd id returns 200 + [] instead of 404,
// silently masking the URL typo. Mirrors the contract /sync/{id} itself
// has been returning 404 for since the beginning.
func (s *SyncHandlers) ListRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if _, err := s.Store.GetTask(r.Context(), id); err != nil {
		if errors.Is(err, sync.ErrTaskNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
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

// SyncTestInput is the JSON shape for POST /api/sync/test. Just the fields
// needed to probe — name / direction / include are irrelevant for a
// reachability check. Auth mirrors SyncTaskInput: either a credential
// reference (resolved here, so the form can test a saved credential without
// retyping its secret) or inline username/password.
type SyncTestInput struct {
	RemoteURL          string `json:"remoteUrl"`
	RemoteCredentialID string `json:"remoteCredentialId"`
	RemoteUsername     string `json:"remoteUsername"`
	RemotePassword     string `json:"remotePassword"`
}

// TestConnection — POST /api/sync/test
//
// Powers the "测试连接" button on the sync-task form. Does NOT require a
// saved task — takes the remote fields the user is currently editing and
// pings `{remoteUrl}/v2/` with the resolved Basic auth, returning a
// structured result the UI renders as an inline Alert.
//
// When remoteCredentialId is set, the credential is resolved through the
// vault first, so testing a reference exercises the same lookup path a run
// would (v0.6.8 SYNC-3); inline fields are ignored in that case, matching
// Validate's mutual exclusion.
//
// Every probe outcome is HTTP 200 with a sync.ProbeResult body — wrong
// creds / 404 / unreachable all land in the body's AuthStatus field.
// Non-2xx only for handler-level problems:
//
//	400 BAD_REQUEST         — decode failure, missing remoteUrl, or an
//	                          unknown credential id
//	503 SERVICE_UNAVAILABLE — a reference was given but no vault is
//	                          configured (REGISTRY_CREDENTIAL_KEY unset)
//	500 INTERNAL            — other vault error
func (s *SyncHandlers) TestConnection(w http.ResponseWriter, r *http.Request) {
	var in SyncTestInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.RemoteURL) == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("remoteUrl is required"))
		return
	}

	username, password := in.RemoteUsername, in.RemotePassword
	if ref := strings.TrimSpace(in.RemoteCredentialID); ref != "" {
		if s.Vault == nil {
			writeError(w, r, http.StatusServiceUnavailable,
				errors.New("credential library unavailable: REGISTRY_CREDENTIAL_KEY is not set"))
			return
		}
		cred, err := s.Vault.Get(ref)
		if err != nil {
			if errors.Is(err, credentials.ErrNotFound) {
				writeError(w, r, http.StatusBadRequest,
					fmt.Errorf("%w (id=%s)", sync.ErrCredentialNotFound, ref))
				return
			}
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		username, password = cred.Username, cred.Password
	}

	result := sync.ProbeConnection(r.Context(), in.RemoteURL, username, password)
	writeJSON(w, http.StatusOK, result)
}

// SyncScheduleInput is the JSON shape POST/PATCH /api/sync/{id}/schedules
// accepts. Mirrors sync.Schedule minus server-managed fields (ID,
// TaskID, NextRunAt, LastRunAt, LastRunID, CreatedAt, UpdatedAt) — the
// server fills those based on the request + cron evaluation.
//
// Timezone is optional and defaults to UTC when omitted (empty
// string). CronExpr is required; Validate rejects empty.
type SyncScheduleInput struct {
	CronExpr string `json:"cronExpr"`
	Timezone string `json:"timezone,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"` // pointer so PATCH can omit
}

// ListSchedules — GET /api/sync/{id}/schedules
//
// Returns every schedule for the task, oldest first. 404 when the task
// itself doesn't exist (the store would happily return [] otherwise,
// which masks typos in the URL).
func (s *SyncHandlers) ListSchedules(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseID(w, r)
	if !ok {
		return
	}
	if _, err := s.Store.GetTask(r.Context(), taskID); err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	schedules, err := s.Store.ScheduleListByTask(r.Context(), taskID, 100)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if schedules == nil {
		schedules = []sync.Schedule{}
	}
	writeJSON(w, http.StatusOK, schedules)
}

// CreateSchedule — POST /api/sync/{id}/schedules
//
// Validates the cron expression and timezone via sync.Schedule.Validate
// and persists. Returns 400 for invalid cron / timezone; 404 for
// unknown task; 201 + the created schedule.
func (s *SyncHandlers) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseID(w, r)
	if !ok {
		return
	}
	if _, err := s.Store.GetTask(r.Context(), taskID); err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	var input SyncScheduleInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	sched := &sync.Schedule{
		TaskID:   taskID,
		CronExpr: input.CronExpr,
		Timezone: input.Timezone,
		Enabled:  enabled,
	}
	if err := s.Store.ScheduleCreate(r.Context(), sched); err != nil {
		if errors.Is(err, sync.ErrInvalidCron) || errors.Is(err, sync.ErrInvalidTimezone) {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, *sched)
}

// UpdateSchedule — PATCH /api/sync/{id}/schedules/{sid}
//
// Merges the partial input onto the existing schedule, re-validates,
// and updates NextRunAt. Same error mapping as create.
func (s *SyncHandlers) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseID(w, r)
	if !ok {
		return
	}
	scheduleID, err := strconv.ParseInt(chi.URLParam(r, "sid"), 10, 64)
	if err != nil || scheduleID <= 0 {
		writeError(w, r, http.StatusBadRequest, fmt.Errorf("sid must be a positive integer"))
		return
	}
	var input SyncScheduleInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	// Read existing so a partial patch keeps unset fields.
	existing, err := s.Store.ScheduleListByTask(r.Context(), taskID, 0)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	var current *sync.Schedule
	for i := range existing {
		if existing[i].ID == scheduleID {
			current = &existing[i]
			break
		}
	}
	if current == nil {
		writeError(w, r, http.StatusNotFound, sync.ErrScheduleNotFound)
		return
	}
	if input.CronExpr != "" {
		current.CronExpr = input.CronExpr
	}
	if input.Timezone != "" || (input.CronExpr != "" && current.Timezone != "") {
		// explicit timezone wins; if cron changed but timezone omitted,
		// keep what was there
		if input.Timezone != "" {
			current.Timezone = input.Timezone
		}
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if err := s.Store.ScheduleUpdate(r.Context(), current); err != nil {
		if errors.Is(err, sync.ErrInvalidCron) || errors.Is(err, sync.ErrInvalidTimezone) {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
		if errors.Is(err, sync.ErrScheduleNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, *current)
}

// DeleteSchedule — DELETE /api/sync/{id}/schedules/{sid}
//
// 404 when the schedule doesn't exist on this task; otherwise 200 + a
// JSON envelope with the deleted id (v0.6.12 MA-5 fix — used to be 204
// No Content, which broke the frontend's envelope parser; see the long
// comment on DeleteTask above for the same story).
func (s *SyncHandlers) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	scheduleID, err := strconv.ParseInt(chi.URLParam(r, "sid"), 10, 64)
	if err != nil || scheduleID <= 0 {
		writeError(w, r, http.StatusBadRequest, fmt.Errorf("sid must be a positive integer"))
		return
	}
	// task_id in the URL is decorative — schedule id is globally unique,
	// and the store's delete-by-id handles the FK guarantee. We still
	// require a valid {id} path param so the URL shape is REST-correct.
	if _, ok := parseID(w, r); !ok {
		return
	}
	if err := s.Store.ScheduleDelete(r.Context(), scheduleID); err != nil {
		if errors.Is(err, sync.ErrScheduleNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	// (task_id match is implicit: schedules live under one task; if the
	// id doesn't belong to taskID, the row was simply not found above.)
	writeJSON(w, http.StatusOK, map[string]any{"id": scheduleID, "deleted": true})
}

// RunItemsPage is the response shape for ListRunItems.
//
// Envelope (items + total + limit + offset) instead of bare []SyncRunItem
// so the UI can render "共 N 条 / 第 M 页" without a separate count
// request. total comes from the COUNT(*) returned by SyncRunItemListByRun
// — cheap thanks to the sync_run_items_run index.
type RunItemsPage struct {
	Items  []sync.SyncRunItem `json:"items"`
	Total  int                `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

// ListRunItems — GET /api/sync/{id}/runs/{rid}/items?limit=50&offset=0
//
// v0.6.16: returns one page of (repo, tag) attempts for one run.
// limit defaults to 50 (matches the runs-list default) and is capped
// at 500 so a runaway client can't ask for the whole history in one
// round trip. offset defaults to 0.
//
// 404 cases:
//   - task id not in DB
//   - run id not in DB
//   - run id belongs to a different task than {id} (cross-tenant
//     guard — the URL says "the run under task {id}" so a run that
//     lives under another task must not be readable here)
//
// Pagination ordering is id ASC (= engine iteration order); the UI
// doesn't need a separate sort.
func (s *SyncHandlers) ListRunItems(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseID(w, r)
	if !ok {
		return
	}
	runID, err := strconv.ParseInt(chi.URLParam(r, "rid"), 10, 64)
	if err != nil || runID <= 0 {
		writeError(w, r, http.StatusBadRequest, errors.New("rid must be a positive integer"))
		return
	}
	if _, err := s.Store.GetTask(r.Context(), taskID); err != nil {
		if errors.Is(err, sync.ErrTaskNotFound) {
			writeError(w, r, http.StatusNotFound, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	// Cross-tenant guard: confirm the run actually belongs to the task
	// in the URL. Without this, /api/sync/1/runs/999/items would happily
	// dump items belonging to task 2's runs.
	runs, err := s.Store.ListRunsByTask(r.Context(), taskID, 0)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	runBelongs := false
	for _, run := range runs {
		if run.ID == runID {
			runBelongs = true
			break
		}
	}
	if !runBelongs {
		writeError(w, r, http.StatusNotFound, sync.ErrRunNotFound)
		return
	}

	limit := 50
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if limit > 500 {
		limit = 500
	}

	items, total, err := s.Store.ListRunItems(r.Context(), runID, limit, offset)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if items == nil {
		items = []sync.SyncRunItem{}
	}
	writeJSON(w, http.StatusOK, RunItemsPage{
		Items:  items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}
