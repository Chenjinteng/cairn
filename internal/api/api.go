package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Chenjinteng/cairn/internal/config"
	"github.com/Chenjinteng/cairn/internal/registry"
	"github.com/Chenjinteng/cairn/internal/webui"
)

// chiURLParam is a tiny shim so handler code reads cleaner than chi.URLParam.
// Equivalent to chi.URLParam(r, key); kept here to avoid importing chi in
// every handler file.
func chiURLParam(r *http.Request, key string) string {
	return chi.URLParam(r, key)
}

// Sentinel errors used by handlers. Centralized so /api/tags 403/400 messages
// are stable and easy to grep in the frontend.
var (
	errDeleteDisabled      = errors.New("delete is disabled (allow.delete=false)")
	errMissingParam        = errors.New("missing required query parameter")
	errRepoRequired        = errors.New("repository name required")
	errRepoNotFound        = errors.New("repository not found")
	errUnsupportedRepoPath = errors.New("unsupported repository path")
)

// registryErr is a typed-nil alias for type assertions in handlers.
// We don't currently export it; handlers use errors.As directly with
// *registry.Error for clarity.
var _ = registry.Manifest{} // keep the import in case we add registry-aware helpers here later

// NewRouter is the v0.1 entry point (no extras). Kept for backwards compat
// with the existing api_test.go tests.
func NewRouter(h *Handlers, cfg *config.Config) http.Handler {
	return NewRouterWithExtras(h, nil, cfg)
}

// NewRouterWithExtras assembles the chi mux with all v0.1 + v0.2 + v0.3 + v0.3
// (registry) routes wired. extras may be nil (e.g. in tests that only cover v0.1).
//
// Returns the chi.Router so callers can Mount additional sub-routers
// (e.g. /v2/* registryd). For http.Handler-only consumers, the same chi
// mux satisfies http.Handler.
func NewRouterWithExtras(h *Handlers, extras *ExtraHandlers, cfg *config.Config) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(slogLogger(cfg))
	if cfg.Env == "dev" {
		r.Use(corsDev)
	}

	// Liveness / readiness
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// /readyz requires the storage to be readable.
		if h.Store != nil {
			if _, err := h.Store.Repositories(r.Context()); err != nil {
				writeError(w, r, http.StatusServiceUnavailable, err)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	// API surface
	r.Route("/api", func(r chi.Router) {
		r.Get("/config", h.GetConfig)
		r.Patch("/config", h.UpdateConfig)
		r.Post("/probe", h.Probe)
		r.Get("/inventory", h.GetInventory)
		r.Delete("/tags", h.DeleteTag)

		// v0.6.12 (MA-1): /api/repositories/{repo}* uses a wildcard
		// dispatcher instead of chi's "{repo}" named param — repo names
		// naturally contain "/" and chi can't span it. See
		// internal/api/dispatch.go and docs/issues/management-api.md MA-1.
		// GET is registered here so v0.1 callers (extras==nil) still get
		// the manifest endpoint; DELETE goes through extras below.
		r.Get("/repositories/*", func(w http.ResponseWriter, req *http.Request) {
			dispatchRepositoriesRoute(w, req, h, extras)
		})

		if extras != nil {
			r.Delete("/repositories/*", func(w http.ResponseWriter, req *http.Request) {
				dispatchRepositoriesRoute(w, req, h, extras)
			})
			extras.RegisterRoutes(r)
		}
	})

	// Top-level webhook alias: registry notification configs commonly point
	// at /events; the /api/events route registered above stays for compat.
	if extras != nil && extras.Events != nil {
		r.Post("/events", extras.Events.ServeHTTP)
	}

	// Embedded SPA (compiled in with -tags webui). The catch-all has the
	// lowest routing priority: /api/*, /v2/*, /healthz, /readyz all win.
	r.Handle("/*", webui.Handler())

	return r
}

// slogLogger emits a single structured log line per request via slog.
// Replaces chi's stdlib-Logger so we keep one JSON log stream end to end.
//
// v0.6.32 (O2 — log level split): the line level now reflects the outcome
// instead of always being Info:
//
//   - 5xx                 → slog.Error    (operator wants to grep these
//                                           out first; tail latency + 5xx
//                                           are the two "page me" signals)
//   - 4xx                 → slog.Warn     (still client-facing errors,
//                                           not a server fault)
//   - 2xx / 3xx on writes → slog.Info     (read 2xx are below — same
//                                           level, distinct kind=write tag
//                                           so operators can grep "writes
//                                           only" without losing noise
//                                           from background GETs)
//   - 2xx / 3xx on reads  → slog.Debug    (production noise floor: image
//                                           tags list / stats / etc. all
//                                           return 200, every request —
//                                           promote to Debug so Info stays
//                                           meaningful)
//
// Two extra fields are added on every line:
//
//   - actor: the value of the X-Auth-User header set by the V2 basic-auth
//     middleware after a successful credential match, or "-" for /api/*
//     traffic (no auth yet) and unauthenticated V2 calls. Operators can
//     group audit-friendly lines by actor without enabling the heavier
//     audit-log machinery.
//   - kind: "read" or "write", the HTTP verb bucketed into "mutates
//     state" vs "doesn't". Anything other than GET / HEAD is "write";
//     OPTIONS stays "read" since it's a CORS preflight, not an action.
func slogLogger(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)
			next.ServeHTTP(ww, req)

			status := ww.Status()
			level := slog.LevelInfo
			switch {
			case status >= 500:
				level = slog.LevelError
			case status >= 400:
				level = slog.LevelWarn
			case status >= 200 && (req.Method != http.MethodGet && req.Method != http.MethodHead):
				// 2xx/3xx on a mutating verb — keep at Info so writes
				// don't drown in the noise floor. (See godoc.)
				level = slog.LevelInfo
			case status >= 200:
				// 2xx/3xx on a read verb — most cairn traffic is this.
				// Drop to Debug so the Info channel isn't 99% /api/inventory.
				level = slog.LevelDebug
			}
			kind := "read"
			if req.Method != http.MethodGet && req.Method != http.MethodHead && req.Method != http.MethodOptions {
				kind = "write"
			}
			actor := req.Header.Get("X-Auth-User")
			if actor == "" {
				actor = "-"
			}
			attrs := []any{
				"method", req.Method,
				"path", req.URL.Path,
				"status", status,
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
				"env", cfg.Env,
				"kind", kind,
				"actor", actor,
			}
			slog.Log(req.Context(), level, "http", attrs...)
		})
	}
}

// corsDev is the relaxed CORS policy used only when CAIRN_ENV=dev.
// (v0.5.23: GO_HUB_ENV → CAIRN_ENV)
// Production deploys are expected to terminate CORS at a reverse proxy.
func corsDev(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
