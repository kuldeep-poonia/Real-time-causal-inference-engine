package config

import (
	"os"
	"strconv"
)

// Config holds all runtime-configurable parameters for ABSIA.
// Values are read once at startup from environment variables.
type Config struct {
	// PrometheusURL is the base URL of the Prometheus server to poll.
	// Example: "http://prometheus:9090"
	// If empty, the pipeline falls back to synthetic data with a logged warning.
	PrometheusURL string

	// PrometheusQuery is the PromQL expression whose results populate the
	// causal graph nodes. Each unique label set becomes one node.
	// Default: rate(container_cpu_usage_seconds_total[1m])
	PrometheusQuery string

	// StepSeconds controls the Prometheus query_range step and the Poller tick
	// interval. Smaller values give finer time resolution at higher API cost.
	// Default: 15
	StepSeconds int

	// MinDataPoints is the minimum number of time-series points required per
	// node before the pipeline considers the data ready for causal analysis.
	// Default: 20
	MinDataPoints int

	// ProcessorWindowSize is the rolling-window size passed to each phase1
	// Processor. Older points beyond this window are evicted.
	// Default: 500
	ProcessorWindowSize int

	// ProcessorAlpha is the EMA smoothing factor for phase1 Processors.
	// Range (0,1]. Closer to 1 = less smoothing.
	// Default: 0.1
	ProcessorAlpha float64

	// APIKey is the bearer token required on sensitive mutation endpoints (/act).
	// When empty, authentication is disabled (development mode — log a warning).
	// Set via ABSIA_API_KEY environment variable.
	APIKey string

	// PolicyStorePath is the directory where trained RL policy weights are
	// persisted between pipeline runs. Enables warmstart training instead of
	// random re-initialization on every request.
	// Default: /tmp/absia_policies
	PolicyStorePath string

	// MaxBodyBytes is the maximum allowed request body size in bytes.
	// Requests larger than this are rejected with HTTP 413.
	// Default: 1048576 (1 MiB)
	MaxBodyBytes int64

	// Seed is the base seed for all deterministic random operations.
	// Pass 0 to default to 42. Override via ABSIA_SEED for varied exploration.
	// Default: 42
	Seed int64

	// LogLevel controls structured log verbosity: "debug", "info", "warn", "error".
	// Default: "info"
	LogLevel string

	// ReadTimeoutSeconds is the HTTP server read timeout.
	// Default: 5
	ReadTimeoutSeconds int

	// WriteTimeoutSeconds is the HTTP server write timeout.
	// Pipeline can take up to ~2s; 30s gives ample headroom.
	// Default: 30
	WriteTimeoutSeconds int

	// IdleTimeoutSeconds is the HTTP server keep-alive idle timeout.
	// Default: 120
	IdleTimeoutSeconds int

	// RateLimitRequestsPerSecond is the sustained request rate allowed per
	// remote IP across all pipeline endpoints (/analyze, /explain, /act, /ingest).
	// 0 disables rate limiting (development mode).
	// Default: 10
	RateLimitRequestsPerSecond int

	// RateLimitBurst is the maximum instantaneous burst above the sustained
	// rate. Must be >= 1 when rate limiting is enabled.
	// Default: 20
	RateLimitBurst int
}

// Load reads configuration from environment variables, applying defaults
// for any variable that is absent or unparseable.
func Load() Config {
	query := getenv("PROMETHEUS_QUERY", `rate(container_cpu_usage_seconds_total[1m])`)

	seed := getenvInt64("ABSIA_SEED", 42)
	if seed <= 0 {
		seed = 42
	}

	return Config{
		PrometheusURL:       getenv("PROMETHEUS_URL", ""),
		PrometheusQuery:     query,
		StepSeconds:         getenvInt("PROMETHEUS_STEP_SECONDS", 15),
		MinDataPoints:       getenvInt("ABSIA_MIN_DATA_POINTS", 20),
		ProcessorWindowSize: getenvInt("ABSIA_PROCESSOR_WINDOW", 500),
		ProcessorAlpha:      getenvFloat("ABSIA_PROCESSOR_ALPHA", 0.1),
		APIKey:              getenv("ABSIA_API_KEY", ""),
		PolicyStorePath:     getenv("ABSIA_POLICY_STORE_PATH", "/tmp/absia_policies"),
		MaxBodyBytes:        getenvInt64("ABSIA_MAX_BODY_BYTES", 1<<20), // 1 MiB
		Seed:                seed,
		LogLevel:            getenv("ABSIA_LOG_LEVEL", "info"),
		ReadTimeoutSeconds:         getenvInt("ABSIA_READ_TIMEOUT_SECONDS", 5),
		WriteTimeoutSeconds:        getenvInt("ABSIA_WRITE_TIMEOUT_SECONDS", 30),
		IdleTimeoutSeconds:         getenvInt("ABSIA_IDLE_TIMEOUT_SECONDS", 120),
		RateLimitRequestsPerSecond: getenvInt("ABSIA_RATE_LIMIT_RPS", 10),
		RateLimitBurst:             getenvInt("ABSIA_RATE_LIMIT_BURST", 20),
	}
}

// HasPrometheus returns true when a Prometheus URL is configured.
// When false, the pipeline runs in synthetic-data fallback mode.
func (c Config) HasPrometheus() bool {
	return c.PrometheusURL != ""
}

// AuthEnabled returns true when API key authentication is active.
func (c Config) AuthEnabled() bool {
	return c.APIKey != ""
}

// ---- helpers ----------------------------------------------------------------

func getenv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getenvInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultVal
	}
	return n
}

func getenvInt64(key string, defaultVal int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return defaultVal
	}
	return n
}

func getenvFloat(key string, defaultVal float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return defaultVal
	}
	return f
}
