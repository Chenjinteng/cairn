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
	errDeleteDisabled = errors.New("delete is disabled (REGISTRY_ALLOW_DELETE=false)")
	errMissingParam   = errors.New("missing required query parameter")
)

// registryErr is a typed-nil alias for type assertions in handlers.
// We don't currently export it; handlers use errors.As directly with
// *registry.Error for clarity.
var _ = registry.Manifest{} // keep the import in case we add registry-aware helpers here later

// NewRouter assembles the chi mux with all v0.1 routes wired.
//
// Route layout (matches registry-manager so the React frontend works):
//
//	GET    /api/config             PublicConfig
//	POST   /api/probe             connectivity check
//	GET    /api/inventory         full inventory (cached)
//	POST   /api/refresh           force re-scan
//	DELETE /api/tags              delete manifest by digest (?repo&digest)
//	GET    /api/repositories/{repo}/tags/{tag}/manifest   per-tag detail
//
// Middleware stack: RequestID -> RealIP -> Recoverer -> Logger.
// No auth in v0.1 (matches registry-manager; see security note in README).
func NewRouter(h *Handlers, cfg *config.Config) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(slogLogger(cfg))
	if cfg.Env == "dev" {
		r.Use(corsDev)
	}

	// Liveness / readiness
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// For v0.1 we treat "registry reachable" as readiness.
		if err := h.Registry.Probe(r.Context()); err != nil {
			writeError(w, r, http.StatusServiceUnavailable, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	// v0.1 API surface
	r.Route("/api", func(r chi.Router) {
		r.Get("/config", h.GetConfig)
		r.Post("/probe", h.Probe)
		r.Get("/inventory", h.GetInventory)
		r.Post("/refresh", h.RefreshInventory)
		r.Delete("/tags", h.DeleteTag)
		r.Get("/repositories/{repo}/tags/{tag}/manifest", h.GetManifest)
	})

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