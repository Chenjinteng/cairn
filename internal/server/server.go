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
	"time"

	"github.com/go-chi/chi/v5"

	"cairn/internal/api"
	"cairn/internal/config"
	"cairn/internal/credentials"
	"cairn/internal/db"
	"cairn/internal/events"
	"cairn/internal/proxies"
	"cairn/internal/pull"
	"cairn/internal/registry"
	"cairn/internal/registryd"
	"cairn/internal/storage"
)

// Runtime bundles the long-lived dependencies.
type Runtime struct {
	HTTP       *http.Server
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
	dataDir := cfg.CredentialsDir
	if dataDir == "" {
		dataDir = "/app/data"
	}

	// 1. Storage layer — cairn's own filesystem backend.
	store, err := storage.NewFilesystem(filepath.Join(dataDir, "registry"))
	if err != nil {
		return nil, fmt.Errorf("server: open storage: %w", err)
	}

	// 2. SQLite for stats + history (best-effort; if it fails, /api/stats returns 503).
	var store_db *db.Db
	if dbPath, err := safeDBPath(dataDir); err == nil {
		d, err := db.Open(dbPath)
		if err != nil {
			slog.Warn("db open failed; stats endpoints will 503", "err", err)
		} else {
			store_db = d
		}
	}

	// 3. Credential vault.
	vault, err := credentials.Open(filepath.Join(dataDir, "credentials.json"), cfg.CredentialKey)
	if err != nil {
		return nil, fmt.Errorf("server: open vault: %w", err)
	}

	// 4. Proxy store.
	proxyStore, err := proxies.Open(filepath.Join(dataDir, "proxies.json"))
	if err != nil {
		return nil, fmt.Errorf("server: open proxies: %w", err)
	}

	// 5. External registry client (used by pull jobs to fetch from upstream).
	var externalRegistry registry.Registry
	if cfg.RegistryURL != "" {
		c, err := registry.NewClient(registry.Config{
			BaseURL:  cfg.RegistryURL,
			Username: cfg.RegistryUsername,
			Password: cfg.RegistryPassword,
			Proxy:    cfg.RegistryProxy,
			Timeout:  60 * time.Second,
		})
		if err != nil {
			slog.Warn("external registry client init failed; pull-from-external disabled", "err", err)
		} else {
			externalRegistry = registry.NewCached(c, cfg.CacheTTL)
		}
	}

	// 6. Pull executor (only meaningful if external registry is reachable).
	var executor *pull.Executor
	if externalRegistry != nil {
		orchestrator := &pull.Orchestrator{
			Dest:                 store,
			ExternalRegistry:     externalRegistry,
			SrcResolve:           pull.DefaultSourceResolver(),
			Vault:                vault,
			Proxies:              proxyStore,
			DB:                   store_db,
			PullHistoryRetention: cfg.PullHistoryRetentionDay,
		}
		executor = pull.NewExecutor(cfg.PullQueueSize, orchestrator.RunOne)
	}

	// 7. Events handler.
	var eventsHandler *events.Handler
	if store_db != nil && cfg.NotifyToken != "" {
		eventsHandler = events.NewHandler(store_db, cfg.NotifyToken, cfg.StatsIgnoreUserAgents, 200)
	}

	// 8. Admin handlers (browse/delete talk to local storage; pull uses external client).
	handlers := &api.Handlers{Cfg: cfg, Store: store, Registry: externalRegistry}

	extras := &api.ExtraHandlers{
		Cfg: &api.ConfigExtras{
			AllowPull:        cfg.AllowPull,
			IgnoreUserAgents: cfg.StatsIgnoreUserAgents,
		},
		Executor: executor,
		Vault:    vault,
		Proxies:  proxyStore,
		DB:       store_db,
		Events:   eventsHandler,
		Registry: externalRegistry,
		Store:    store,
	}

	// 9. Composite router: /api/* + /v2/*
	apiMux := api.NewRouterWithExtras(handlers, extras, cfg)
	// apiMux is a *chi.Mux (http.Handler that satisfies chi.Router); mount
	// the registryd sub-router under /v2/*.
	chiRouter := apiMux.(chi.Router)
	chiRouter.Mount("/v2", registryd.New(store))
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
	slog.Info("server built",
		"port", cfg.Port,
		"data_dir", dataDir,
		"cache_ttl", cfg.CacheTTL,
		"db", store_db != nil,
		"external_registry", externalRegistry != nil,
	)

	pullCtx, pullCancel := context.WithCancel(context.Background())

	return &Runtime{
		HTTP:       srv,
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