// Command server is the cairn HTTP entry point.
//
// Loads config, builds the runtime (registry + db + vault + proxy store +
// pull executor + events handler), listens on :PORT until SIGTERM/SIGINT,
// then drains in-flight requests with a 30s grace period.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Chenjinteng/cairn/internal/config"
	"github.com/Chenjinteng/cairn/internal/server"
	"github.com/Chenjinteng/cairn/internal/version"
)

func main() {
	// -healthz: Docker HEALTHCHECK probe mode. Instead of starting a second
	// server (which collided on :8787 and always exited 1 — the container
	// was permanently "unhealthy" in v0.3.x), dial the running instance's
	// /healthz and map the result onto the exit code.
	healthz := flag.Bool("healthz", false, "probe http://127.0.0.1:$PORT/healthz of the running server and exit 0 on success")
	flag.Parse()
	if *healthz {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8787"
		}
		client := &http.Client{Timeout: 4 * time.Second}
		resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
		if err != nil {
			fmt.Fprintf(os.Stderr, "healthz probe failed: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "healthz probe got status %d\n", resp.StatusCode)
			os.Exit(1)
		}
		os.Exit(0)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(2)
	}
	slog.Info("config loaded",
		"version", version.Version,
		"port", cfg.Port,
		"env", cfg.Env,
		"credentials_dir", cfg.CredentialsDir,
		"storage_dir", cfg.StorageDir,
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
