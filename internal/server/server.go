// Package server wires config -> registry -> api together and produces a
// runnable *http.Server. main.go only needs to call Build and ListenAndServe.
package server

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"cairn/internal/api"
	"cairn/internal/config"
	"cairn/internal/registry"
)

// Build constructs the http.Handler + *http.Server.
//
//   - cfg is the resolved runtime config (Load'd in main)
//   - returns (handler, server, error); handler is also mounted on server.Handler
func Build(cfg *config.Config) (http.Handler, *http.Server, error) {
	client, err := registry.NewClient(registry.Config{
		BaseURL:  cfg.RegistryURL,
		Username: cfg.RegistryUsername,
		Password: cfg.RegistryPassword,
		Proxy:    cfg.RegistryProxy,
		Timeout:  60 * time.Second,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("server: build registry client: %w", err)
	}

	cached := registry.NewCached(client, cfg.CacheTTL)
	handlers := &api.Handlers{
		Cfg:      cfg,
		Registry: cached,
		Cached:   cached,
	}

	mux := api.NewRouter(handlers, cfg)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // 0 = no timeout; pull jobs may stream blobs for minutes
		IdleTimeout:       120 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	slog.Info("server built",
		"port", cfg.Port,
		"registry", cfg.RegistryURL,
		"cache_ttl", cfg.CacheTTL,
	)
	return mux, srv, nil
}