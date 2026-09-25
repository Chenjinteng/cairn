// Command server is the cairn HTTP entry point.
//
// Loads config, builds the HTTP handler, listens on :PORT until SIGTERM/SIGINT,
// then drains in-flight requests with a 30s grace period.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cairn/internal/config"
	"cairn/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(2)
	}
	slog.Info("config loaded",
		"registry_url", cfg.RegistryURL,
		"registry_name", cfg.RegistryName,
		"port", cfg.Port,
		"env", cfg.Env,
		"allow_delete", cfg.AllowDelete,
		"allow_pull", cfg.AllowPull,
	)

	_, srv, err := server.Build(cfg)
	if err != nil {
		slog.Error("server build failed", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining")
	case err := <-errCh:
		slog.Error("http server crashed", "err", err)
		os.Exit(1)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	slog.Info("http server stopped")
}