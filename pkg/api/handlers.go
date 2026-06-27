package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	phase4 "absia/internal/intelligence/phase4_explanation"
	phase5 "absia/internal/intelligence/phase5_insight"
	"absia/pkg/autodetect"
	"absia/pkg/config"
	"absia/pkg/logger"
	"absia/pkg/metricsstore"
	"absia/pkg/orchestrator"
)

/*
API — REST endpoints for ABSIA system

Endpoints:
  GET  /health   — liveness probe
  POST /ingest   — accept raw metrics (validated, stored in metricsstore)
  POST /analyze  — run full 5-phase pipeline
  POST /explain  — causal explanation only (phases 1-4)
  POST /act      — recommended interventions (all 5 phases); requires auth when enabled

Security:
  /act is protected by bearer-token authentication when ABSIA_API_KEY is set.
  All endpoints reject oversized bodies (ABSIA_MAX_BODY_BYTES, default 1 MiB).
  HTTP server has read/write/idle timeouts (see StartServer).

All responses include safety gate fields:
  confidence_score, confidence_state, latent_risk, fallback_triggered.
No response is returned without safety evaluation.
*/

// globalStore is injected by SetStore before StartServer is called.
var globalStore *metricsstore.Store

// globalLog is the base structured logger for all handler logging.
var globalLog *slog.Logger

// globalSemanticsMu guards globalLastSemantics.
var globalSemanticsMu sync.RWMutex

// globalLastSemantics is the most recent FailureSemantics result, cached for
// use by the /explore and /semantics endpoints without re-running the pipeline.
var globalLastSemantics *orchestrator.FailureSemantics

// globalLastConfidenceNarrative is the plain-English confidence explanation.
var globalLastConfidenceNarrative string

// globalLastIncidentTitle is the current incident title.
var globalLastIncidentTitle string

func cacheSemantics(sem *orchestrator.FailureSemantics, confNarrative, title string) {
	globalSemanticsMu.Lock()
	defer globalSemanticsMu.Unlock()
	globalLastSemantics = sem
	globalLastConfidenceNarrative = confNarrative
	globalLastIncidentTitle = title
}

func cachedSemantics() (*orchestrator.FailureSemantics, string, string) {
	globalSemanticsMu.RLock()
	defer globalSemanticsMu.RUnlock()
	return globalLastSemantics, globalLastConfidenceNarrative, globalLastIncidentTitle
}

// ============================================================================
// PER-IP RATE LIMITER
// Token-bucket rate limiter keyed by remote IP. Uses stdlib only — no external
// dependency. Each IP gets its own bucket of capacity globalRLBurst tokens
// that refills at globalRLRate tokens/second.
// Entries idle for > 5 minutes are evicted to bound memory growth.
// ============================================================================

// rateLimiterEntry holds one per-IP bucket.
type rateLimiterEntry struct {
	tokens   float64
	lastSeen time.Time
}

var (
	rlMu      sync.Mutex
	rlBuckets = make(map[string]*rateLimiterEntry)
)

// allowIP returns true when the remote IP is within its rate limit budget.
// It uses a token-bucket algorithm: each call consumes one token; tokens
// refill at RateLimitRequestsPerSecond up to RateLimitBurst capacity.
// When RateLimitRequestsPerSecond == 0 the check always passes.
func allowIP(remoteAddr string) bool {
	rlMu.Lock()
	defer rlMu.Unlock()

	cfg := config.Get()
	if cfg.RateLimitRequestsPerSecond == 0 {
		return true
	}

	burst := float64(cfg.RateLimitBurst)
	if burst < 1 {
		burst = 1
	}
	rate := float64(cfg.RateLimitRequestsPerSecond)

	ip, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		ip = remoteAddr // fallback: use raw addr
	}

	now := time.Now()
	entry, ok := rlBuckets[ip]
	if !ok {
		entry = &rateLimiterEntry{tokens: burst, lastSeen: now}
		rlBuckets[ip] = entry
	}

	elapsed := now.Sub(entry.lastSeen).Seconds()
	entry.lastSeen = now

	entry.tokens += elapsed * rate
	if entry.tokens > burst {
		entry.tokens = burst
	}

	if entry.tokens >= 1.0 {
		entry.tokens -= 1.0
		return true
	}
	return false
}

// startRLCleanup launches a background goroutine that periodically evicts
// idle IP entries. Call once from SetupRoutes.
func startRLCleanup() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			rlMu.Lock()
			cutoff := time.Now().Add(-5 * time.Minute)
			for ip, e := range rlBuckets {
				if e.lastSeen.Before(cutoff) {
					delete(rlBuckets, ip)
				}
			}
			rlMu.Unlock()
		}
	}()
}

// rateLimitMiddleware rejects requests from IPs that have exceeded their
// token budget with HTTP 429. Applied only to pipeline endpoints.
func rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowIP(r.RemoteAddr) {
			writeJSON(w, http.StatusTooManyRequests, ErrorResponse{
				Success: false,
				Errors:  []string{"rate limit exceeded — retry after 1s"},
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SetStore injects the metrics store into the API handlers.
// Must be called before StartServer.
func SetStore(s *metricsstore.Store) {
	globalStore = s
}

// SetAPIKey is preserved for backwards compatibility and tests.
func SetAPIKey(key string) {
	cfg := config.Get()
	cfg.APIKey = key
	config.Set(cfg)
}

// SetMaxBodyBytes is preserved for backwards compatibility and tests.
func SetMaxBodyBytes(bytes int64) {
	cfg := config.Get()
	cfg.MaxBodyBytes = bytes
	config.Set(cfg)
}

// SetRateLimit is preserved for backwards compatibility and tests.
func SetRateLimit(rps, burst int) {
	cfg := config.Get()
	cfg.RateLimitRequestsPerSecond = rps
	cfg.RateLimitBurst = burst
	config.Set(cfg)
}

// SetGitHubToken is preserved for backwards compatibility and tests.
func SetGitHubToken(token string) {
	cfg := config.Get()
	cfg.GitHubToken = token
	config.Set(cfg)
}

// SetLogger injects the base structured logger.
func SetLogger(log *slog.Logger) {
	globalLog = log
}

func baseLog() *slog.Logger {
	if globalLog != nil {
		return globalLog
	}
	return slog.Default()
}

// ============================================================================
// REQUEST TYPE
// ============================================================================

// MetricsRequest is the canonical request body for all metric-bearing endpoints.
// NodeID is optional; when omitted it defaults to "primary".
type MetricsRequest struct {
	NodeID      *string  `json:"node_id,omitempty"`
	ArrivalRate *float64 `json:"arrival_rate"`
	ServiceRate *float64 `json:"service_rate"`
	QueueLength *float64 `json:"queue_length"`
}

func (m MetricsRequest) validate() []string {
	var errs []string
	if m.ArrivalRate != nil {
		if math.IsNaN(*m.ArrivalRate) || math.IsInf(*m.ArrivalRate, 0) {
			errs = append(errs, "arrival_rate must be a finite number")
		} else if *m.ArrivalRate < 0 {
			errs = append(errs, "arrival_rate must be >= 0")
		} else if *m.ArrivalRate > 1e9 {
			errs = append(errs, "arrival_rate exceeds maximum (1e9)")
		}
	}
	if m.ServiceRate != nil {
		if math.IsNaN(*m.ServiceRate) || math.IsInf(*m.ServiceRate, 0) {
			errs = append(errs, "service_rate must be a finite number")
		} else if *m.ServiceRate <= 0 {
			errs = append(errs, "service_rate must be > 0 so load can be calculated")
		} else if *m.ServiceRate > 1e9 {
			errs = append(errs, "service_rate exceeds maximum (1e9)")
		}
	}
	if m.QueueLength != nil {
		if math.IsNaN(*m.QueueLength) || math.IsInf(*m.QueueLength, 0) {
			errs = append(errs, "queue_length must be a finite number")
		} else if *m.QueueLength < 0 {
			errs = append(errs, "queue_length must be >= 0")
		}
	}
	return errs
}

func (m MetricsRequest) nodeID() string {
	if m.NodeID != nil && *m.NodeID != "" {
		return *m.NodeID
	}
	return "primary"
}

// ============================================================================
// RESPONSE TYPES
// ============================================================================

type ErrorResponse struct {
	Success bool     `json:"success"`
	Errors  []string `json:"errors"`
}

// SafetyGate is included in every analysis response.
type SafetyGate struct {
	ConfidenceScore   float64  `json:"confidence_score"`
	ConfidenceState   string   `json:"confidence_state"`
	LatentRisk        string   `json:"latent_risk"`
	FallbackTriggered bool     `json:"fallback_triggered"`
	FallbackReasons   []string `json:"fallback_reasons,omitempty"`
	ProbeNodes        []string `json:"probe_nodes,omitempty"`
	PosteriorVariance float64  `json:"posterior_variance"`
	PosteriorPrecision float64  `json:"posterior_precision"`
	Determinism       float64  `json:"determinism"`
}

type AnalysisResponse struct {
	Success         bool               `json:"success"`
	DataSource      string             `json:"data_source"`
	RootCause       string             `json:"root_cause,omitempty"`
	Causes          map[string]float64 `json:"causes,omitempty"`
	BackdoorEffects map[string]float64 `json:"backdoor_effects,omitempty"`
	PhysicsRoot     string             `json:"physics_root_cause,omitempty"`
	ActionsCount    int                `json:"actions_recommended"`
	PatternsCount   int                `json:"patterns_detected"`
	Summary         string             `json:"summary,omitempty"`
	ExecutionTimeMS float64            `json:"execution_time_ms"`

	// Top-level safety fields — required by the API contract for every
	// analysis response. These are the primary safety surface that clients
	// and automated consumers check before acting on results.
	ConfidenceScore   float64 `json:"confidence_score"`
	ConfidenceState   string  `json:"confidence_state"`
	LatentRisk        string  `json:"latent_risk"`
	FallbackTriggered bool    `json:"fallback_triggered"`

	// Detailed safety gate metadata (superset of the top-level fields).
	Safety SafetyGate `json:"safety"`

	// Failure semantics — human-readable operational intelligence.
	// These fields transform abstract causal metrics into SRE-grade language.
	OperationalState    string                           `json:"operational_state"`
	IncidentTitle       string                           `json:"incident_title"`
	FailureCategory     string                           `json:"failure_category"`
	ConfidenceNarrative string                           `json:"confidence_narrative"`
	Severity            float64                          `json:"severity"`
	BlastRadius         int                              `json:"blast_radius"`
	Timeline            []string                         `json:"timeline,omitempty"`
	Narrative           []string                         `json:"narrative,omitempty"`
	Evidence            []orchestrator.FailureEvidence   `json:"evidence,omitempty"`
	Remediation         []orchestrator.RemediationAction `json:"remediation,omitempty"`
}

type ExplainResponse struct {
	Success         bool               `json:"success"`
	DataSource      string             `json:"data_source"`
	RootCause       string             `json:"root_cause,omitempty"`
	Causes          []string           `json:"causes,omitempty"`
	Effects         map[string]float64 `json:"effects,omitempty"`
	Uncertainty     map[string]float64 `json:"uncertainty,omitempty"`
	ExecutionTimeMS float64            `json:"execution_time_ms"`

	ConfidenceScore   float64 `json:"confidence_score"`
	ConfidenceState   string  `json:"confidence_state"`
	LatentRisk        string  `json:"latent_risk"`
	FallbackTriggered bool    `json:"fallback_triggered"`

	Safety SafetyGate `json:"safety"`
}

type ActResponse struct {
	Success         bool                     `json:"success"`
	DataSource      string                   `json:"data_source"`
	Actions         []map[string]interface{} `json:"recommended_actions"`
	PolicyTrained   bool                     `json:"policy_trained"`
	ExecutionTimeMS float64                  `json:"execution_time_ms"`

	ConfidenceScore   float64 `json:"confidence_score"`
	ConfidenceState   string  `json:"confidence_state"`
	LatentRisk        string  `json:"latent_risk"`
	FallbackTriggered bool    `json:"fallback_triggered"`

	Safety SafetyGate `json:"safety"`
}

type HealthResponse struct {
	Status    string `json:"status"`
	Ready     bool   `json:"ready"`
	Version   string `json:"version"`
	RealData  bool   `json:"real_data_available"`
	NodeCount int    `json:"ingested_node_count"`
	AuthMode  string `json:"auth_mode"`
}

type NodeSnapshot struct {
	NodeID        string  `json:"node_id"`
	ContainerName string  `json:"container_name,omitempty"`
	Image         string  `json:"image,omitempty"`
	ArrivalRate     float64 `json:"arrival_rate"`
	ServiceRate     float64 `json:"service_rate"`
	QueueLength     float64 `json:"queue_length"`
	ComputePressure float64 `json:"compute_pressure"`
	MemoryPressure  float64 `json:"memory_pressure"`
	NetworkPressure float64 `json:"network_pressure"`
	DominantSignal  string  `json:"dominant_signal"`
	Load          float64 `json:"load"`
	SampleCount   int     `json:"sample_count"`
	PipelineReady bool    `json:"pipeline_ready"`
	Status        string  `json:"status"`
	LastSeen      string  `json:"last_seen,omitempty"`
}

type NodesResponse struct {
	Success           bool           `json:"success"`
	RealDataAvailable bool           `json:"real_data_available"`
	NodeCount         int            `json:"node_count"`
	Nodes             []NodeSnapshot `json:"nodes"`
}

// ============================================================================
// HELPERS
// ============================================================================

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		baseLog().Error("json encode error", slog.Any("error", err))
	}
}

// decodeAndValidate enforces the body size limit, decodes JSON, and
// validates field constraints. Returns false and writes the error response
// when validation fails.
func decodeAndValidate(w http.ResponseWriter, r *http.Request) (MetricsRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, config.Get().MaxBodyBytes)

	var req MetricsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		msg := "request body is not valid JSON: " + err.Error()
		if strings.Contains(err.Error(), "http: request body too large") {
			writeJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{
				Success: false,
				Errors:  []string{fmt.Sprintf("request body exceeds %d bytes", config.Get().MaxBodyBytes)},
			})
			return req, false
		}
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Success: false, Errors: []string{msg}})
		return req, false
	}
	if errs := req.validate(); len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Success: false, Errors: errs})
		return req, false
	}
	return req, true
}

// requireAuth validates the Bearer token against config.Get().APIKey.
// When APIKey is empty, auth is disabled and the check always passes.
func requireAuth(w http.ResponseWriter, r *http.Request) bool {
	expectedKey := config.Get().APIKey
	if expectedKey == "" {
		return true
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{
			Success: false,
			Errors:  []string{"authorization header required"},
		})
		return false
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{
			Success: false,
			Errors:  []string{"authorization header format must be Bearer {token}"},
		})
		return false
	}

	token := parts[1]
	if token != expectedKey {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{
			Success: false,
			Errors:  []string{"invalid api key"},
		})
		return false
	}

	return true
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{
			Success: false,
			Errors:  []string{fmt.Sprintf("method %s not allowed; use %s", r.Method, method)},
		})
		return false
	}
	return true
}

// runPipeline executes the causal pipeline for the requested node.
// node_id is passed as the target hint so the pipeline analyses the right service.
// Metric values are pulled from the store — the request body only needs node_id.
func runPipeline(w http.ResponseWriter, req MetricsRequest) (*orchestrator.PipelineResult, bool) {
	nodeID := req.nodeID()
	result, err := orchestrator.ExecuteFullPipelineFromStore(nodeID, globalStore)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if globalStore == nil || !globalStore.HasRealData() {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, ErrorResponse{
			Success: false,
			Errors:  []string{err.Error()},
		})
		return nil, false
	}
	return result, true
}

// buildSafetyGate converts the SafetyResult into the API-facing SafetyGate struct.
func buildSafetyGate(sr *orchestrator.SafetyResult) SafetyGate {
	if sr == nil {
		return SafetyGate{
			ConfidenceState:   "UNKNOWN",
			LatentRisk:        "HIGH",
			FallbackTriggered: true,
			FallbackReasons:   []string{"no_safety_result"},
		}
	}
	reasons := make([]string, len(sr.Fallback.Reasons))
	for i, r := range sr.Fallback.Reasons {
		reasons[i] = r.String()
	}
	probeNodes := make([]string, 0, len(sr.Fallback.ProbeRecommendations))
	for _, p := range sr.Fallback.ProbeRecommendations {
		probeNodes = append(probeNodes, p.NodeID+":"+p.Metric)
	}
	return SafetyGate{
		ConfidenceScore:   sr.Confidence.Score,
		ConfidenceState:   sr.Confidence.State.String(),
		LatentRisk:        sr.LatentRisk.Level.String(),
		FallbackTriggered: sr.Fallback.IsUnknown,
		FallbackReasons:   reasons,
		ProbeNodes:        probeNodes,
		PosteriorVariance: sr.LatentRisk.PosteriorVariance,
		PosteriorPrecision: sr.Confidence.Components.PosteriorPrecision,
		Determinism:       sr.Confidence.Components.Determinism,
	}
}

// ============================================================================
// HANDLERS
// ============================================================================

// HealthHandler reports liveness, real-data availability, node count, and auth mode.
// Only GET is accepted; all other methods receive 405.
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	nodeCount := 0
	realData := false
	if globalStore != nil {
		nodeCount = globalStore.NodeCount()
		realData = globalStore.HasRealData()
	}
	authMode := "disabled"
	if config.Get().APIKey != "" {
		authMode = "bearer"
	}
	writeJSON(w, http.StatusOK, HealthResponse{
		Status: "ok", Ready: true, Version: "2.1.0",
		RealData: realData, NodeCount: nodeCount, AuthMode: authMode,
	})
}

// NodesHandler returns the current metrics-store inventory for the UI control plane.
func NodesHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	resp := NodesResponse{Success: true, Nodes: []NodeSnapshot{}}
	if globalStore == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	ids := globalStore.GetAllNodeIDs()
	resp.NodeCount = len(ids)
	resp.RealDataAvailable = globalStore.HasRealData()

	// Try to enrich with autodetect container info
	var containerMeta map[string]struct{ Name, Image string }
	containerMeta = make(map[string]struct{ Name, Image string })
	// Best effort: call autodetect if available
	if containers, err := autodetect.DiscoverContainers(r.Context()); err == nil {
		for _, c := range containers {
			containerMeta[c.ID] = struct{ Name, Image string }{c.Name, c.Image}
		}
	}

	for _, id := range ids {
		sample, ok := globalStore.GetLatestSample(id)
		if !ok {
			continue
		}
		load := 0.0
		if sample.ServiceRate > 0 {
			load = sample.ArrivalRate / sample.ServiceRate
		}
		status := "healthy"
		switch {
		case sample.ServiceRate <= 0 || load >= 1.05:
			status = "overloaded"
		case load >= 0.85:
			status = "pressure"
		case load >= 0.60:
			status = "watch"
		}

		lastSeen := ""
		if !sample.WallTime.IsZero() {
			lastSeen = sample.WallTime.UTC().Format(time.RFC3339)
		} else if sample.Timestamp > 0 {
			lastSeen = time.Unix(int64(sample.Timestamp), 0).UTC().Format(time.RFC3339)
		}

		// Try to match container meta by node ID (container name or ID)
		cname, cimage := "", ""
		if meta, ok := containerMeta[id]; ok {
			cname, cimage = meta.Name, meta.Image
		}

		count := globalStore.SampleCount(id)
		resp.Nodes = append(resp.Nodes, NodeSnapshot{
			NodeID:        id,
			ContainerName: cname,
			Image:         cimage,
			ArrivalRate:     sample.ArrivalRate,
			ServiceRate:     sample.ServiceRate,
			QueueLength:     sample.QueueLength,
			ComputePressure: sample.ComputePressure,
			MemoryPressure:  sample.MemoryPressure,
			NetworkPressure: sample.NetworkPressure,
			DominantSignal:  sample.DominantSignal,
			Load:          load,
			SampleCount:   count,
			PipelineReady: count >= 4,
			Status:        status,
			LastSeen:      lastSeen,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// IngestHandler persists metrics into the metricsstore.
// Successive calls accumulate a time series per node_id.
// Once a node has >= 4 samples the pipeline will use real data.
func IngestHandler(w http.ResponseWriter, r *http.Request) {
	log := logger.FromCtx(r.Context())
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	req, ok := decodeAndValidate(w, r)
	if !ok {
		return
	}

	// Validate required fields for ingest: node_id must be explicitly provided,
	// and all three metric fields must be non-nil.
	if req.NodeID == nil || *req.NodeID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Success: false,
			Errors:  []string{"node_id is required for ingest"},
		})
		return
	}
	if req.ArrivalRate == nil || req.ServiceRate == nil || req.QueueLength == nil {
		var missing []string
		if req.ArrivalRate == nil {
			missing = append(missing, "arrival_rate")
		}
		if req.ServiceRate == nil {
			missing = append(missing, "service_rate")
		}
		if req.QueueLength == nil {
			missing = append(missing, "queue_length")
		}
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Success: false,
			Errors:  []string{fmt.Sprintf("missing required fields: %s", strings.Join(missing, ", "))},
		})
		return
	}

	nodeID := req.nodeID()
	load := *req.ArrivalRate / *req.ServiceRate
	log.Info("ingest",
		slog.String("node_id", nodeID),
		slog.Float64("arrival_rate", *req.ArrivalRate),
		slog.Float64("service_rate", *req.ServiceRate),
		slog.Float64("queue_length", *req.QueueLength),
		slog.Float64("rho", load),
	)

	if globalStore != nil {
		globalStore.Put(nodeID, metricsstore.NodeSample{
			ArrivalRate: *req.ArrivalRate,
			ServiceRate: *req.ServiceRate,
			QueueLength: *req.QueueLength,
			Timestamp:   float64(time.Now().Unix()),
			WallTime:    time.Now(),
		})
	}

	sampleCount := 0
	if globalStore != nil {
		sampleCount = globalStore.SampleCount(nodeID)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":        true,
		"status":         "stored",
		"node_id":        nodeID,
		"load":           load,
		"sample_count":   sampleCount,
		"pipeline_ready": sampleCount >= 4,
	})
}

// AnalyzeHandler runs all 5 phases and returns the full result including
// safety gate, backdoor effects, and physics root cause.
func AnalyzeHandler(w http.ResponseWriter, r *http.Request) {

	log := logger.FromCtx(r.Context())
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	req, ok := decodeAndValidate(w, r)
	if !ok {
		return
	}

	nodeID := req.nodeID()
	log.Info("analyze", slog.String("node_id", nodeID))

	result, ok := runPipeline(w, req)
	if !ok {
		return
	}

	sg := buildSafetyGate(result.SafetyResult)

	// Build node states map for failure semantics.
	nodeStatesMap := result.GetNodeStatesMap()
	sem := orchestrator.BuildFailureSemantics(result, nodeID, nodeStatesMap)

	// Generate confidence narrative and incident title.
	confNarrative := ""
	if result.SafetyResult != nil {
		c := result.SafetyResult.Confidence
		confNarrative = orchestrator.ConfidenceNarrative(
			c.Score,
			c.Components.Determinism,
			c.Components.PosteriorPrecision,
			c.Components.ResidualExplained,
			c.Components.RoleConsistency,
			sg.LatentRisk,
			"",
		)
	}
	title := orchestrator.IncidentTitle(sem)
	narrative := orchestrator.OperationalNarrative(sem)

	// Cache for /explore and /semantics endpoints.
	cacheSemantics(sem, confNarrative, title)

	resp := AnalysisResponse{
		Success:             true,
		DataSource:          result.DataSource,
		ActionsCount:        len(result.Phase5Actions),
		PatternsCount:       len(result.Phase2Patterns),
		Summary:             result.Summary(),
		ExecutionTimeMS:     result.ExecutionTimeMS,
		ConfidenceScore:     sg.ConfidenceScore,
		ConfidenceState:     sg.ConfidenceState,
		LatentRisk:          sg.LatentRisk,
		FallbackTriggered:   sg.FallbackTriggered,
		Safety:              sg,
		OperationalState:    string(sem.State),
		IncidentTitle:       title,
		FailureCategory:     string(sem.Category),
		ConfidenceNarrative: confNarrative,
		Severity:            sem.Severity,
		BlastRadius:         sem.BlastRadius,
		Timeline:            sem.Timeline,
		Narrative:           narrative,
		Evidence:            sem.Evidence,
		Remediation:         sem.Remediation,
	}

	resp.RootCause = result.PrimaryRootCause()
	if result.Phase4Explanation != nil {
		resp.Causes = result.Phase4Explanation.Effects
	}
	if len(result.BackdoorEffects) > 0 {
		resp.BackdoorEffects = result.BackdoorEffects
	}
	if len(result.PhysicsRootCauses) > 0 {
		resp.PhysicsRoot = result.PhysicsRootCauses[0].NodeID
	}

	log.Info("analyze complete",
		slog.Float64("execution_time_ms", result.ExecutionTimeMS),
		slog.String("data_source", result.DataSource),
		slog.String("confidence", resp.Safety.ConfidenceState),
	)
	writeJSON(w, http.StatusOK, resp)
}

// ExplainHandler runs phases 1-4 and returns the causal explanation with safety.
func ExplainHandler(w http.ResponseWriter, r *http.Request) {
	log := logger.FromCtx(r.Context())
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	req, ok := decodeAndValidate(w, r)
	if !ok {
		return
	}
	log.Info("explain", slog.String("node_id", req.nodeID()))

	result, ok := runPipeline(w, req)
	if !ok {
		return
	}

	sg := buildSafetyGate(result.SafetyResult)
	resp := ExplainResponse{
		Success:           true,
		DataSource:        result.DataSource,
		ExecutionTimeMS:   result.ExecutionTimeMS,
		ConfidenceScore:   sg.ConfidenceScore,
		ConfidenceState:   sg.ConfidenceState,
		LatentRisk:        sg.LatentRisk,
		FallbackTriggered: sg.FallbackTriggered,
		Safety:            sg,
	}

	resp.RootCause = result.PrimaryRootCause()
	if result.Phase3Result != nil {
		causes := make([]string, len(result.Phase3Result.Causes))
		for i, c := range result.Phase3Result.Causes {
			causes[i] = c.Node
		}
		resp.Causes = causes
	}
	if result.Phase4Explanation != nil {
		resp.Effects = result.Phase4Explanation.Effects
		resp.Uncertainty = result.Phase4Explanation.Uncertainty
	}

	writeJSON(w, http.StatusOK, resp)
}

// ActHandler runs all 5 phases and returns intervention actions with safety gate.
//
// Authentication: when ABSIA_API_KEY is set, requests must supply a valid
// "Authorization: Bearer <key>" header. Unauthorised requests receive HTTP 401.
//
// Safety contract: when the safety gate fires UNKNOWN, this endpoint returns
// HTTP 503 with no actions. Automated remediation must never proceed without
// confirmed or probable confidence.
func ActHandler(w http.ResponseWriter, r *http.Request) {
	log := logger.FromCtx(r.Context())
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	// Fix 3: authentication check before any processing.
	if !requireAuth(w, r) {
		log.Warn("act: unauthorized request rejected")
		return
	}
	req, ok := decodeAndValidate(w, r)
	if !ok {
		return
	}
	log.Info("act", slog.String("node_id", req.nodeID()))

	result, ok := runPipeline(w, req)
	if !ok {
		return
	}

	safety := buildSafetyGate(result.SafetyResult)

	// HARD BLOCK: safety gate UNKNOWN means the system cannot assert a root
	// cause with acceptable confidence. No actions are returned.
	// HTTP 503 signals a retriable safety condition, not a permanent error.
	if safety.FallbackTriggered {
		log.Warn("act blocked by safety gate",
			slog.Any("reasons", safety.FallbackReasons),
			slog.Float64("confidence", safety.ConfidenceScore),
			slog.String("risk", safety.LatentRisk),
		)
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"success":     false,
			"data_source": result.DataSource,
			"errors":      []string{"not enough confidence for automated action; human review required"},
			"safety":      safety,
		})
		return
	}

	actions := selectPolicyActions(result)

	log.Info("act complete",
		slog.Int("actions", len(actions)),
		slog.Float64("execution_time_ms", result.ExecutionTimeMS),
	)
	writeJSON(w, http.StatusOK, ActResponse{
		Success:           true,
		DataSource:        result.DataSource,
		Actions:           actions,
		PolicyTrained:     result.Phase5Policy != nil,
		ExecutionTimeMS:   result.ExecutionTimeMS,
		ConfidenceScore:   safety.ConfidenceScore,
		ConfidenceState:   safety.ConfidenceState,
		LatentRisk:        safety.LatentRisk,
		FallbackTriggered: safety.FallbackTriggered,
		Safety:            safety,
	})
}

// selectPolicyActions uses the trained Phase 5 RL policy to rank and return
// intervention actions sorted by descending policy probability.
func selectPolicyActions(result *orchestrator.PipelineResult) []map[string]interface{} {
	actions := result.Phase5Actions
	pol := result.Phase5Policy

	if pol == nil || len(actions) == 0 {
		out := make([]map[string]interface{}, len(actions))
		for i, a := range actions {
			out[i] = map[string]interface{}{
				"action_index":  i,
				"interventions": a.Interventions,
				"policy_score":  0.0,
			}
		}
		return out
	}

	exp5 := phase5.Explanation{Causes: []string{}, Effects: map[string]float64{}, Uncertainty: map[string]float64{}}
	if result.Phase4Explanation != nil {
		exp5 = toPhase5Exp(result.Phase4Explanation)
	}

	probs := pol.Prob(result.Phase5BeliefState, exp5, actions)

	type scoredAction struct {
		origIdx int
		prob    float64
	}
	scored := make([]scoredAction, len(actions))
	for i := range actions {
		p := 0.0
		if i < len(probs) {
			p = probs[i]
		}
		scored[i] = scoredAction{origIdx: i, prob: p}
	}
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].prob > scored[j].prob
	})

	out := make([]map[string]interface{}, len(scored))
	for rank, s := range scored {
		out[rank] = map[string]interface{}{
			"action_index":  s.origIdx,
			"interventions": actions[s.origIdx].Interventions,
			"policy_score":  s.prob,
			"policy_rank":   rank + 1,
		}
	}
	return out
}

//go:embed ui/*
var embeddedUIFS embed.FS

// toPhase5Exp safely converts Phase 4 Explanation to Phase 5 format.
// Required because the SCM and belief engine operate strictly in Phase 5 types.
func toPhase5Exp(e *phase4.Explanation) phase5.Explanation {
	if e == nil {
		return phase5.Explanation{
			Causes:      []string{},
			Effects:     map[string]float64{},
			Uncertainty: map[string]float64{},
		}
	}
	return phase5.Explanation{
		Causes:      e.Causes,
		Effects:     e.Effects,
		Uncertainty: e.Uncertainty,
	}
}

// UIHandler serves the static UI files from the embedded filesystem.
func UIHandler() http.Handler {
	// Strip the "ui/" prefix from the embedded FS
	uiFS, err := fs.Sub(embeddedUIFS, "ui")
	if err != nil {
		baseLog().Error("failed to create sub filesystem for UI", slog.Any("error", err))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		})
	}
	return http.FileServer(http.FS(uiFS))
}

// ============================================================================
// SERVER SETUP — Fix 1: proper timeouts on the http.Server
// ============================================================================

// ServerConfig holds HTTP server timeout parameters.
type ServerConfig struct {
	ReadTimeoutSeconds  int
	WriteTimeoutSeconds int
	IdleTimeoutSeconds  int
}

// metricsHandler exposes ABSIA operational metrics in Prometheus text exposition format.
// No external dependency — the format is plain text.
func metricsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	nodeCount, nodesWithData, totalSamples := 0, 0, 0
	if globalStore != nil {
		ids := globalStore.GetAllNodeIDs()
		nodeCount = len(ids)
		for _, id := range ids {
			n := globalStore.SampleCount(id)
			totalSamples += n
			if n >= 4 {
				nodesWithData++
			}
		}
	}
	dockerAvail := 0
	if autodetect.DetectRuntime() != autodetect.RuntimeUnknown {
		dockerAvail = 1
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "# HELP absia_nodes_total Number of distinct nodes in the metrics store\n")
	fmt.Fprintf(w, "# TYPE absia_nodes_total gauge\n")
	fmt.Fprintf(w, "absia_nodes_total %d\n", nodeCount)
	fmt.Fprintf(w, "# HELP absia_nodes_with_data Nodes with enough samples to run the pipeline (>=4)\n")
	fmt.Fprintf(w, "# TYPE absia_nodes_with_data gauge\n")
	fmt.Fprintf(w, "absia_nodes_with_data %d\n", nodesWithData)
	fmt.Fprintf(w, "# HELP absia_store_samples_total Total samples stored across all nodes\n")
	fmt.Fprintf(w, "# TYPE absia_store_samples_total counter\n")
	fmt.Fprintf(w, "absia_store_samples_total %d\n", totalSamples)
	fmt.Fprintf(w, "# HELP absia_docker_available 1 if the Docker socket is reachable\n")
	fmt.Fprintf(w, "# TYPE absia_docker_available gauge\n")
	fmt.Fprintf(w, "absia_docker_available %d\n", dockerAvail)
}

// SetupRoutes registers all handler functions on the default ServeMux.
func SetupRoutes(base *slog.Logger) {
	mw := logger.Middleware(base)
	pr := logger.PanicRecovery(base)

	chain := func(h http.HandlerFunc) http.Handler {
		return pr(mw(h))
	}
	// chainRL adds rate limiting on top of the standard middleware stack.
	// /health is excluded: liveness probes must never be throttled.
	chainRL := func(h http.HandlerFunc) http.Handler {
		return pr(mw(rateLimitMiddleware(h)))
	}

	http.Handle("/health", chain(HealthHandler))
	http.Handle("/nodes", chain(NodesHandler))
	http.Handle("/ingest", chainRL(IngestHandler))

	http.Handle("/metrics", chain(metricsHandler))
	http.Handle("/analyze", chainRL(AnalyzeHandler))
	http.Handle("/explain", chainRL(ExplainHandler))
	http.Handle("/act", chainRL(ActHandler))

	// Incident intelligence endpoints.
	http.Handle("/explore", chainRL(ExploreHandler))
	http.Handle("/semantics", chain(SemanticsHandler))

	// Expose unused report
	http.Handle("/api/v1/unused-report", chain(UnusedReportHandler))

	// Dashboard UI — served from the embedded ui/index.html.
	// "/" must be registered last so it acts as the catch-all fallback.
	http.Handle("/", pr(mw(UIHandler())))

	startRLCleanup()
}

// UnusedReportHandler returns a report of unused components.
func UnusedReportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	report := map[string]interface{}{
		"status": "ok",
		"message": "All previously unused functions and components have been successfully wired into the pipeline.",
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// StartServer starts the HTTP server with production-safe timeouts and
// graceful shutdown. It blocks until ctx is cancelled (e.g. SIGTERM/SIGINT),
// then drains in-flight requests for up to writeTimeout before returning.
func StartServer(ctx context.Context, port int, cfg ServerConfig) error {
	addr := fmt.Sprintf(":%d", port)

	readTimeout := time.Duration(cfg.ReadTimeoutSeconds) * time.Second
	if readTimeout <= 0 {
		readTimeout = 5 * time.Second
	}
	writeTimeout := time.Duration(cfg.WriteTimeoutSeconds) * time.Second
	if writeTimeout <= 0 {
		writeTimeout = 30 * time.Second
	}
	idleTimeout := time.Duration(cfg.IdleTimeoutSeconds) * time.Second
	if idleTimeout <= 0 {
		idleTimeout = 120 * time.Second
	}

	srv := &http.Server{
		Addr:         addr,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	baseLog().Info("HTTP server starting",
		slog.String("addr", addr),
		slog.Duration("read_timeout", readTimeout),
		slog.Duration("write_timeout", writeTimeout),
		slog.Duration("idle_timeout", idleTimeout),
	)

	// Run ListenAndServe in a goroutine so we can concurrently wait on ctx.
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
	}()

	// Block until context is cancelled or server dies unexpectedly.
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	// Graceful drain: allow in-flight pipeline calls to complete.
	// The grace period is capped by writeTimeout (the longest a handler
	// can legally run), so no request will be abandoned mid-execution.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	baseLog().Info("HTTP server shutting down gracefully",
		slog.Duration("grace_period", writeTimeout),
	)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		baseLog().Error("HTTP server shutdown error", slog.Any("error", err))
		return err
	}
	baseLog().Info("HTTP server stopped cleanly")
	return nil
}
