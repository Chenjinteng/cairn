// Command server is the cairn HTTP entry point.
//
// Loads config, builds the runtime (registry + db + vault + proxy store +
// pull executor + events handler), listens on :PORT until SIGTERM/SIGINT,
// then drains in-flight requests with a 30s grace period.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"cairn/internal/config"
	"cairn/internal/server"
	"cairn/internal/version"
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
		"version", version.Version,
		"registry_url", cfg.RegistryURL,
		"registry_name", cfg.RegistryName,
		"port", cfg.Port,
		"env", cfg.Env,
		"allow_delete", cfg.AllowDelete,
		"allow_pull", cfg.AllowPull,
	)

	rt, err := server.Build(cfg)
	if err != nil {
		slog.Error("server build failed", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := rt.Start(ctx); err != nil {
		slog.Error("server start failed", "err", err)
		os.Exit(1)
	}
	rt.Stop()
}