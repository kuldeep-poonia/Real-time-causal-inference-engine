package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"absia/pkg/api"
	"absia/pkg/autodetect"
	"absia/pkg/config"
	"absia/pkg/logger"
	"absia/pkg/metricsstore"
	"absia/pkg/orchestrator"
	"absia/pkg/policy"
	"absia/pkg/telemetry"
)

func main() {
	cfg := config.Load()

	// ── Structured logger — must be initialised before anything else logs.
	structLog := logger.New(cfg.LogLevel)
	slog.SetDefault(structLog)

	// ── Metrics store — must be initialised before autodetect goroutines
	// so that PushContainerStatsToStore has a valid store reference.
	store := metricsstore.New(60)
	
	// ── Telemetry initialization
	telemetry.SetTracker(telemetry.NewExpvarTracker())

	// ── Docker autodiscovery (plug-and-play, graceful degradation)
	// Checks runtime availability at startup. Order: Docker -> Containerd -> CRI-O -> cgroups.
	runtime := autodetect.DetectRuntime()
	if runtime != autodetect.RuntimeUnknown {
		structLog.Info("Container runtime detected — starting container autodiscovery and metrics collection", slog.String("runtime", string(runtime)))
		bgCtx := context.Background()
		go autodetect.StartContainerDiscovery(bgCtx, structLog)
		go autodetect.PushContainerStatsToStore(bgCtx, store, structLog)
	} else {
		structLog.Warn("No supported container runtime detected — autodiscovery disabled; " +
			"push metrics manually via POST /ingest")
	}

	structLog.Info("ABSIA starting",
		slog.String("version", "2.1.0"),
		slog.String("log_level", cfg.LogLevel),
		slog.Bool("auth_enabled", cfg.AuthEnabled()),
		slog.Int64("seed", cfg.Seed),
	)

	if !cfg.AuthEnabled() {
		structLog.Warn("API key auth disabled — set ABSIA_API_KEY to protect the /act endpoint")
	}

	// ── Deterministic pipeline seed
	orchestrator.SetSeed(cfg.Seed)

	// ── Policy persistence store (optional; warm-starts RL policy between runs)
	ps, err := policy.New(cfg.PolicyStorePath, structLog)
	if err != nil {
		structLog.Warn("policy store unavailable — policies will not persist between restarts",
			slog.String("path", cfg.PolicyStorePath),
			slog.Any("error", err),
		)
	} else {
		orchestrator.SetPolicyStore(ps)
		structLog.Info("policy store ready", slog.String("path", cfg.PolicyStorePath))
	}

	// ── Signal-aware root context for graceful shutdown.
	// SIGTERM is the standard Kubernetes/Docker termination signal.
	// SIGINT covers Ctrl-C in development.
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// ── Wire the metrics store and configuration into the API layer
	api.SetStore(store)
	api.SetLogger(structLog)
	
	// Start config hot-reloading background process
	go config.Watch(rootCtx, 10*time.Second)

	if cfg.RateLimitRequestsPerSecond > 0 {
		structLog.Info("rate limiting enabled",
			slog.Int("rps", cfg.RateLimitRequestsPerSecond),
			slog.Int("burst", cfg.RateLimitBurst),
		)
	} else {
		structLog.Warn("rate limiting disabled — set ABSIA_RATE_LIMIT_RPS to enable")
	}

	// ── HTTP server
	port := 8080
	if p := os.Getenv("PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			port = v
		}
	}

	api.SetupRoutes(structLog)

	structLog.Info("HTTP server starting",
		slog.Int("port", port),
		slog.Bool("auth_enabled", cfg.AuthEnabled()),
	)

	srvCfg := api.ServerConfig{
		ReadTimeoutSeconds:  cfg.ReadTimeoutSeconds,
		WriteTimeoutSeconds: cfg.WriteTimeoutSeconds,
		IdleTimeoutSeconds:  cfg.IdleTimeoutSeconds,
	}

	// StartServer blocks until rootCtx is cancelled (SIGTERM/SIGINT), then
	// drains in-flight requests before returning.
	if err := api.StartServer(rootCtx, port, srvCfg); err != nil {
		log.Fatalf("[ABSIA] Server failed: %v", err)
	}
	structLog.Info("ABSIA shutdown complete")
}