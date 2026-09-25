package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"cairn/internal/config"
	"cairn/internal/registry"
	"cairn/internal/webui"
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
	errDeleteDisabled = errors.New("delete is disabled (allow.delete=false)")
	errMissingParam   = errors.New("missing required query parameter")
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
		r.Post("/refresh", h.RefreshInventory)
		r.Delete("/tags", h.DeleteTag)
		r.Get("/repositories/{repo}/tags/{tag}/manifest", h.GetManifest)

		if extras != nil {
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
func slogLogger(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)
			next.ServeHTTP(ww, req)
			slog.Info("http",
				"method", req.Method,
				"path", req.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
				"env", cfg.Env,
			)
		})
	}
}

// corsDev is the relaxed CORS policy used only when GO_HUB_ENV=dev.
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
