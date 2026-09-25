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

// NewRouter is the v0.1 entry point (no extras). Kept for backwards compat
// with the existing api_test.go tests.
func NewRouter(h *Handlers, cfg *config.Config) http.Handler {
	return NewRouterWithExtras(h, nil, cfg)
}

// NewRouterWithExtras assembles the chi mux with all v0.1 + v0.2 + v0.3
// routes wired. extras may be nil (e.g. in tests that only cover v0.1).
//
// Route layout (matches registry-manager so the React frontend works):
//
//	v0.1:
//	  GET    /api/config             PublicConfig
//	  POST   /api/probe              connectivity check
//	  GET    /api/inventory          full inventory (cached)
//	  POST   /api/refresh            force re-scan
//	  DELETE /api/tags               delete manifest by digest (?repo&digest)
//	  GET    /api/repositories/{repo}/tags/{tag}/manifest   per-tag detail
//
//	v0.2 (pull + credentials + proxies):
//	  GET    /api/pull/jobs          list
//	  POST   /api/pull/jobs          create
//	  GET    /api/pull/jobs/{id}     get one
//	  POST   /api/pull/jobs/{id}/cancel   cancel
//	  DELETE /api/pull/jobs/{id}     delete (terminal only)
//	  POST   /api/pull/probe         test source reachability
//	  GET    /api/credentials/      list
//	  POST   /api/credentials/       create
//	  GET    /api/credentials/{id}   get one (password omitted)
//	  DELETE /api/credentials/{id}   delete
//	  POST   /api/credentials/{id}/test   test connectivity
//	  GET    /api/proxies/           list
//	  POST   /api/proxies/           create
//	  GET    /api/proxies/{id}       get one (password omitted)
//	  DELETE /api/proxies/{id}       delete
//	  POST   /api/proxies/{id}/test  test connectivity
//
//	v0.3 (events + stats):
//	  POST   /api/events             webhook receiver
//	  GET    /api/stats/summary      aggregate totals
//	  GET    /api/stats/top          top repos
//	  GET    /api/stats/series       per-day heatmap points
//	  GET    /api/stats/repositories alias of /top
//	  GET    /api/stats/events       recent debug buffer
//	  GET    /api/stats/clients      per-UA counts
//	  GET    /api/stats/ignore       list ignore patterns
//	  POST   /api/stats/ignore       add pattern
//	  DELETE /api/stats/ignore/{pattern}  remove pattern
//	  DELETE /api/stats/heat         retention cleanup
//
// Middleware stack: RequestID -> RealIP -> Recoverer -> Logger.
// No auth in v0.x (matches registry-manager; see security note in README).
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
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := h.Registry.Probe(r.Context()); err != nil {
			writeError(w, r, http.StatusServiceUnavailable, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	// API surface
	r.Route("/api", func(r chi.Router) {
		r.Get("/config", h.GetConfig)
		r.Post("/probe", h.Probe)
		r.Get("/inventory", h.GetInventory)
		r.Post("/refresh", h.RefreshInventory)
		r.Delete("/tags", h.DeleteTag)
		r.Get("/repositories/{repo}/tags/{tag}/manifest", h.GetManifest)

		if extras != nil {
			extras.RegisterRoutes(r)
		}
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