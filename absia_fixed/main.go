package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"absia/pkg/api"
	"absia/pkg/config"
	"absia/pkg/logger"
	"absia/pkg/metricsstore"
	"absia/pkg/orchestrator"
	"absia/pkg/policy"
	"absia/pkg/realtime"
)

func main() {
	cfg := config.Load()

	// Structured logger — replaces bare log.Printf throughout main.
	structLog := logger.New(cfg.LogLevel)
	slog.SetDefault(structLog)

	structLog.Info("ABSIA starting",
		slog.String("version", "2.1.0"),
		slog.String("log_level", cfg.LogLevel),
		slog.Bool("auth_enabled", cfg.AuthEnabled()),
		slog.Bool("prometheus_enabled", cfg.HasPrometheus()),
		slog.Int64("seed", cfg.Seed),
	)

	if !cfg.AuthEnabled() {
		structLog.Warn("API key authentication is disabled — set ABSIA_API_KEY to secure /act endpoint")
	}

	// ── Configure orchestrator package-level settings ──────────────────────
	orchestrator.SetSeed(cfg.Seed)

	// ── Policy persistence store ───────────────────────────────────────────
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

	// ── Metrics store ──────────────────────────────────────────────────────
	store := metricsstore.New(60)
	api.SetStore(store)
	api.SetAPIKey(cfg.APIKey)
	api.SetMaxBodyBytes(cfg.MaxBodyBytes)
	api.SetLogger(structLog)
	api.SetRateLimit(cfg.RateLimitRequestsPerSecond, cfg.RateLimitBurst)

	if cfg.RateLimitRequestsPerSecond > 0 {
		structLog.Info("rate limiting enabled",
			slog.Int("rps", cfg.RateLimitRequestsPerSecond),
			slog.Int("burst", cfg.RateLimitBurst),
		)
	} else {
		structLog.Warn("rate limiting disabled — set ABSIA_RATE_LIMIT_RPS to enable")
	}

	// ── Signal-aware root context for graceful shutdown ────────────────────
	// SIGTERM is the standard Kubernetes termination signal.
	// SIGINT covers Ctrl-C in development.
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// ── Prometheus poller ──────────────────────────────────────────────────
	if cfg.HasPrometheus() {
		structLog.Info("Prometheus poller enabled", slog.String("url", cfg.PrometheusURL))
		bridge := realtime.NewPollerBridge(cfg.PrometheusURL, store)
		// Poller inherits rootCtx: SIGTERM cancels it cleanly.
		go bridge.Start(rootCtx)
	} else {
		structLog.Warn("PROMETHEUS_URL not set — using synthetic data until /ingest receives >= 4 samples per node")
	}

	// ── Smoke test ─────────────────────────────────────────────────────────
	structLog.Info("running pipeline smoke-test", slog.String("params", "arrival=10, service=8, queue=5"))
	result, err := orchestrator.ExecuteFullPipeline(10.0, 8.0, 5.0)
	if err != nil {
		log.Fatalf("[ABSIA] Pipeline smoke-test failed: %v", err)
	}
	fmt.Println(result.Summary())

	if result.SafetyResult != nil {
		structLog.Info("smoke-test safety gate",
			slog.String("state", result.SafetyResult.Confidence.State.String()),
			slog.Float64("confidence", result.SafetyResult.Confidence.Score),
			slog.String("risk", result.SafetyResult.LatentRisk.Level.String()),
			slog.Bool("fallback", result.SafetyResult.Fallback.IsUnknown),
			slog.Float64("execution_time_ms", result.ExecutionTimeMS),
		)
	}

	// ── HTTP server ────────────────────────────────────────────────────────
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

