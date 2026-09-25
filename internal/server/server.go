// Package server wires config -> registry -> api together and produces a
// runnable *http.Server. main.go only needs to call Build, Start, and Stop.
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"cairn/internal/api"
	"cairn/internal/config"
	"cairn/internal/credentials"
	"cairn/internal/db"
	"cairn/internal/events"
	"cairn/internal/proxies"
	"cairn/internal/pull"
	"cairn/internal/registry"
)

// Runtime bundles the long-lived dependencies that handlers / background
// goroutines reach into. main.go holds a pointer to this so it can call
// Stop() during graceful shutdown.
type Runtime struct {
	HTTP       *http.Server
	DB         *db.Db
	Vault      *credentials.Vault
	Proxies    *proxies.Store
	Executor   *pull.Executor
	Events     *events.Handler
	PullCtx    context.Context
	PullCancel context.CancelFunc
}

// Build wires the full dependency graph and returns a Runtime + ready-to-run
// HTTP server. Returns an error if any subsystem fails to initialize.
func Build(cfg *config.Config) (*Runtime, error) {
	dataDir := cfg.CredentialsDir
	if dataDir == "" {
		dataDir = "/app/data"
	}

	// 1. SQLite (best-effort: if it fails to open, log and continue with
	//    stats endpoints returning 503). Don't crash the whole server —
	//    browse + delete still work.
	var store *db.Db
	if dbPath, err := safeDBPath(dataDir); err == nil {
		d, err := db.Open(dbPath)
		if err != nil {
			slog.Warn("db open failed; stats endpoints will 503", "err", err)
		} else {
			store = d
		}
	}

	// 2. Credential vault.
	vault, err := credentials.Open(filepath.Join(dataDir, "credentials.json"), cfg.CredentialKey)
	if err != nil {
		return nil, fmt.Errorf("server: open vault: %w", err)
	}

	// 3. Proxy store.
	proxyStore, err := proxies.Open(filepath.Join(dataDir, "proxies.json"))
	if err != nil {
		return nil, fmt.Errorf("server: open proxies: %w", err)
	}

	// 4. Destination registry (the one we're managing).
	destClient, err := registry.NewClient(registry.Config{
		BaseURL:  cfg.RegistryURL,
		Username: cfg.RegistryUsername,
		Password: cfg.RegistryPassword,
		Proxy:    cfg.RegistryProxy,
		Timeout:  60 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("server: build registry client: %w", err)
	}
	cached := registry.NewCached(destClient, cfg.CacheTTL)

	// 5. Pull executor with orchestrator.
	orchestrator := &pull.Orchestrator{
		Dest:                   cached,
		SrcResolve:             pull.DefaultSourceResolver(),
		Vault:                  vault,
		Proxies:                proxyStore,
		DB:                     store,
		PullHistoryRetention:   cfg.PullHistoryRetentionDay,
	}
	executor := pull.NewExecutor(cfg.PullQueueSize, orchestrator.RunOne)

	// 6. Events handler.
	var eventsHandler *events.Handler
	if store != nil && cfg.NotifyToken != "" {
		eventsHandler = events.NewHandler(store, cfg.NotifyToken, cfg.StatsIgnoreUserAgents, 200)
	}

	// 7. Handlers + router.
	handlers := &api.Handlers{Cfg: cfg, Registry: cached, Cached: cached}
	extras := &api.ExtraHandlers{
		Cfg: &api.ConfigExtras{
			AllowPull:        cfg.AllowPull,
			IgnoreUserAgents: cfg.StatsIgnoreUserAgents,
		},
		Executor: executor,
		Vault:    vault,
		Proxies:  proxyStore,
		DB:       store,
		Events:   eventsHandler,
		Registry: cached,
	}

	mux := api.NewRouterWithExtras(handlers, extras, cfg)

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
		"registry", cfg.RegistryURL,
		"cache_ttl", cfg.CacheTTL,
		"db", store != nil,
	)

	pullCtx, pullCancel := context.WithCancel(context.Background())

	return &Runtime{
		HTTP:       srv,
		DB:         store,
		Vault:      vault,
		Proxies:    proxyStore,
		Executor:   executor,
		Events:     eventsHandler,
		PullCtx:    pullCtx,
		PullCancel: pullCancel,
	}, nil
}

// Start kicks off background goroutines (pull executor) and runs the HTTP
// server until ctx is cancelled.
func (r *Runtime) Start(ctx context.Context) error {
	go r.Executor.Run(r.PullCtx)

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