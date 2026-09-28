// Package server wires config -> storage + registry client -> API + V2 routes.
// Returns a Runtime that main.go can Start and Stop.
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"cairn/internal/api"
	"cairn/internal/config"
	"cairn/internal/credentials"
	"cairn/internal/db"
	"cairn/internal/events"
	"cairn/internal/proxies"
	"cairn/internal/pull"
	"cairn/internal/registryd"
	"cairn/internal/storage"
)

// Runtime bundles the long-lived dependencies.
type Runtime struct {
	HTTP *http.Server
	// Cfg (v0.5.4) is the live config. Background loops read runtime
	// overrides through it -- stats.retention.days is re-read on every
	// retention pass so a settings-page change applies without a restart.
	Cfg        *config.Config
	DB         *db.Db
	Vault      *credentials.Vault
	Proxies    *proxies.Store
	Store      storage.Storage
	Executor   *pull.Executor
	Events     *events.Handler
	PullCtx    context.Context
	PullCancel context.CancelFunc
}

// Build wires the full dependency graph.
//
// Layer order (data plane first, control plane second):
//  1. Storage (filesystem backend for blobs + manifests + tags)
//  2. V2 router mounted at /v2/* (the actual registry API)
//  3. SQLite (stats + history)
//  4. Credential vault + proxy store
//  5. External registry client (for pull from Docker Hub etc.)
//  6. Pull executor with orchestrator
//  7. Events handler (webhook receiver)
//  8. Admin API + extras (browse/delete/etc.)
//  9. Composite router
func Build(cfg *config.Config) (*Runtime, error) {
	// Load() always fills both fields from the compile-time path
	// constants; the empty guards below only cover hand-built configs
	// (tests that construct a bare config.Config).
	dataDir := cfg.CredentialsDir
	if dataDir == "" {
		dataDir = config.DataDirPath
	}
	storageDir := cfg.StorageDir
	if storageDir == "" {
		storageDir = config.StorageDirPath
	}

	// 1. Storage layer — cairn's own filesystem backend.
	store, err := storage.NewFilesystem(storageDir)
	if err != nil {
		return nil, fmt.Errorf("server: open storage: %w", err)
	}

	// 2. SQLite for stats + history (best-effort; if it fails, /api/stats returns 503).
	var store_db *db.Db
	var dbErr string
	if dbPath, err := safeDBPath(dataDir); err == nil {
		d, err := db.Open(dbPath)
		if err != nil {
			dbErr = err.Error()
			slog.Warn("db open failed; stats endpoints will 503", "err", err)
		} else {
			store_db = d
		}
	} else {
		dbErr = err.Error()
		slog.Warn("db path rejected; stats endpoints will 503", "err", err)
	}

	// 3. Credential vault (best-effort since v0.4.0: a missing/broken
	// REGISTRY_CREDENTIAL_KEY degrades /api/credentials to 503 with the
	// reason instead of taking the whole server down).
	var vault *credentials.Vault
	var vaultErr string
	if v, err := credentials.Open(filepath.Join(dataDir, "credentials.json"), cfg.CredentialKey); err != nil {
		vaultErr = err.Error()
		slog.Warn("credential vault open failed; /api/credentials will 503", "err", err)
	} else {
		vault = v
	}

	// 4. Proxy store (best-effort, same rationale as the vault).
	var proxyStore *proxies.Store
	var proxiesErr string
	if ps, err := proxies.Open(filepath.Join(dataDir, "proxies.json")); err != nil {
		proxiesErr = err.Error()
		slog.Warn("proxy store open failed; /api/proxies will 503", "err", err)
	} else {
		proxyStore = ps
	}

	// 5. Pull executor (always present in v0.5.0; per-job source is part of
	// the request, not a server-wide setting. REGISTRY_URL merely seeds
	// DefaultSourceURL for jobs that don't pin their own upstream).
	// v0.5.2: hydrate every MutableKey from SQLite at startup so previous
	// settings-page edits survive restart. Env is the bootstrap default
	// when the row is absent.
	if store_db != nil && cfg.Mutable != nil {
		n := 0
		for _, key := range config.MutableKeys {
			if v, err := store_db.GetSetting(context.Background(), key); err == nil && v != "" {
				cfg.Mutable.Set(key, v)
				n++
			}
		}
		if n > 0 {
			slog.Info("loaded runtime-mutable settings", "count", n)
		}
	}
	orchestrator := &pull.Orchestrator{
		Dest:       store,
		SrcResolve: pull.DefaultSourceResolver(),
		Vault:      vault,
		Proxies:    proxyStore,
		DB:         store_db,
		// v0.5.1: pull source now flows through cfg.Mutable, so changes
		// via PATCH /api/config take effect on the next queued job.
		Mutable: cfg.Mutable,
		// v0.5.4: live cfg pointer so resolveSource can read the global
		// HTTP proxy (registry.proxy setting). nil-safe.
		Cfg:                  cfg,
		PullHistoryRetention: cfg.PullHistoryRetentionDays(),
	}
	executor := pull.NewExecutor(50, orchestrator.RunOne)

	// 7. Events handler (webhook receiver for registry notifications + the
	// sink for built-in registry self-reports).
	//
	// v0.5.8: construction no longer requires NotifyToken. The handler is
	// built whenever SQLite is up; the built-in registry feeds it
	// directly via registryd.Handler.Events.IngestLocal, no shared secret
	// required. NotifyToken, when empty, simply makes /api/events reject
	// every POST (VerifySignature("", ...) = false → 401 fail-closed) —
	// which matches the existing settings-page wording for operators who
	// haven't wired an external registry.
	//
	// allow.registry_events is still a live predicate via SetEnabled, so
	// flipping it on the settings page stops/starts ingestion without a
	// restart (kill switch, env or panel; the panel toggle was removed
	// in v0.5.8 but the underlying state survives in SQLite for an escape
	// hatch).
	var eventsHandler *events.Handler
	if store_db != nil {
		// effective ignore = env ∪ panel (panel rules persist in SQLite and
		// are merged in at startup so they survive restarts).
		ignore := cfg.StatsIgnoreUserAgents()
		if panel, err := store_db.ListIgnore(context.Background()); err == nil {
			ignore = events.MergeIgnore(ignore, panel)
		}
		eventsHandler = events.NewHandler(store_db, cfg.NotifyToken(), ignore, 200)
		eventsHandler.SetEnabled(cfg.AllowRegistryEvents)
	}

	// 8. Admin handlers (browse/delete talk to local storage; pull uses external client).
	handlers := &api.Handlers{
		Cfg:        cfg,
		Store:      store,
		Vault:      vault,
		Proxies:    proxyStore,
		DB:         store_db,
		VaultErr:   vaultErr,
		ProxiesErr: proxiesErr,
		DBErr:      dbErr,
	}

	extras := &api.ExtraHandlers{
		Full: cfg,
		// v0.5.4: only the genuinely env-only field is snapshotted here.
		// The gates themselves read cfg.Effective*() per request via Full.
		Cfg: &api.ConfigExtras{
			IgnoreUserAgents: cfg.StatsIgnoreUserAgents(),
		},
		Executor:   executor,
		Vault:      vault,
		Proxies:    proxyStore,
		DB:         store_db,
		Events:     eventsHandler,
		Store:      store,
		VaultErr:   vaultErr,
		ProxiesErr: proxiesErr,
		DBErr:      dbErr,
	}

	// 9. Composite router: /api/* + /v2/*
	apiMux := api.NewRouterWithExtras(handlers, extras, cfg)
	// apiMux is a *chi.Mux (http.Handler that satisfies chi.Router); mount
	// the registryd sub-router under /v2/*.
	chiRouter := apiMux.(chi.Router)
	chiRouter.Mount("/v2", registryd.New(store, func() (string, string) { return cfg.RegistryUsername(), cfg.RegistryPassword() }, eventsHandler))
	mux := apiMux.(http.Handler)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // pull jobs may stream blobs for minutes
		IdleTimeout:       120 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	// v0.5.4: log the values actually in force (SQLite overrides are
	// already hydrated above), not the env bootstrap. cache_ttl is gone
	// from this line on purpose -- it has no consumer any more.
	slog.Info("server built",
		"port", cfg.Port,
		"data_dir", dataDir,
		"db", store_db != nil,
		"allow_delete", cfg.AllowDelete(),
		"allow_pull", cfg.AllowPull(),
		"registry_events", cfg.AllowRegistryEvents(),
		"stats_retention_days", cfg.StatsRetentionDays(),
		"default_upstream", strings.TrimRight(cfg.RegistryURL(), "/"),
	)

	pullCtx, pullCancel := context.WithCancel(context.Background())

	return &Runtime{
		HTTP:       srv,
		Cfg:        cfg,
		DB:         store_db,
		Vault:      vault,
		Proxies:    proxyStore,
		Store:      store,
		Executor:   executor,
		Events:     eventsHandler,
		PullCtx:    pullCtx,
		PullCancel: pullCancel,
	}, nil
}

// Start kicks off background goroutines and runs the HTTP server.
func (r *Runtime) Start(ctx context.Context) error {
	if r.Executor != nil {
		go r.Executor.Run(r.PullCtx)
	}
	// v0.5.4: stats.retention.days finally has a consumer. Before this,
	// db.RetentionCleanup existed but nothing ever called it, so
	// activity_daily grew without bound no matter what the setting said.
	if r.DB != nil && r.Cfg != nil {
		go r.retentionLoop(r.PullCtx)
	}
	// v0.5.9: background proxy reachability loop. Probes each registered
	// proxy every 60s so the management page can show live status without
	// forcing operators to wait for a failed pull to find out the entry
	// is dead. Boot kicks one probe so the UI never lingers on "unknown".
	// Lifecycle piggy-backs on r.PullCtx (cancelled in Stop).
	if r.Proxies != nil {
		go func() {
			if err := r.Proxies.ProbeAll(r.PullCtx); err != nil {
				slog.Info("proxy probe at boot: at least one entry failed", "err", err)
			}
			r.Proxies.StartProbeLoop(r.PullCtx, 60*time.Second)
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server starting", "addr", r.HTTP.Addr)
		if err := r.HTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining")
	case err := <-errCh:
		return err
	}
	return nil
}

// retentionLoop prunes activity_daily rows older than the runtime
// stats.retention.days setting. It runs once shortly after startup (so a
// restart applies the current value) and then every 24h. The setting is
// re-read on each pass, so changing it on the settings page takes effect on
// the next pass without a restart.
//
// The DB call gets its own short-lived context: using r.PullCtx directly
// would turn a normal shutdown into a scary "cleanup failed" warning.
func (r *Runtime) retentionLoop(ctx context.Context) {
	const interval = 24 * time.Hour

	timer := time.NewTimer(30 * time.Second) // first pass: let startup settle
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}

		days := r.Cfg.StatsRetentionDays()
		if days <= 0 {
			days = 365
		}
		cutoff := time.Now().UTC().AddDate(0, 0, -days)

		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		n, err := r.DB.RetentionCleanup(cleanupCtx, cutoff)
		cancel()

		if err != nil {
			slog.Warn("stats retention cleanup failed", "err", err, "cutoff", cutoff.Format("2006-01-02"))
		} else if n > 0 {
			slog.Info("stats retention cleanup",
				"deleted", n,
				"cutoff", cutoff.Format("2006-01-02"),
				"retention_days", days)
		}
		timer.Reset(interval)
	}
}

// Stop tears down background goroutines + the HTTP server with a 30s grace.
func (r *Runtime) Stop() {
	r.PullCancel()
	if r.HTTP != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = r.HTTP.Shutdown(shutdownCtx)
	}
	if r.DB != nil {
		_ = r.DB.Close()
	}
	slog.Info("http server stopped")
}

func safeDBPath(dataDir string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("no data dir")
	}
	return filepath.Join(dataDir, "cairn.db"), nil
}

// silence unused import if chi isn't used elsewhere
var _ = chi.NewRouter
