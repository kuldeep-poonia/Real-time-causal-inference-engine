// Package integration contains end-to-end tests that exercise the full
// HTTP API surface including the ingest → accumulate → analyze flow.
// These tests use httptest.NewServer so no real port binding is required.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"absia/pkg/api"
	"absia/pkg/metricsstore"
	"absia/pkg/orchestrator"
)

// setup creates a fresh metricsstore and registers all routes on a new ServeMux
// so tests are fully isolated from each other and from the global default mux.
func setup(t *testing.T, apiKey string) (*httptest.Server, *metricsstore.Store) {
	t.Helper()

	store := metricsstore.New(60)
	orchestrator.SetSeed(42)

	api.SetStore(store)
	api.SetAPIKey(apiKey)
	api.SetMaxBodyBytes(1 << 20)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", api.HealthHandler)
	mux.HandleFunc("/nodes", api.NodesHandler)
	mux.HandleFunc("/ingest", api.IngestHandler)
	mux.HandleFunc("/analyze", api.AnalyzeHandler)
	mux.HandleFunc("/explain", api.ExplainHandler)
	mux.HandleFunc("/act", api.ActHandler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, store
}

// postJSON sends a POST request with a JSON body and returns the response.
func postJSON(t *testing.T, srv *httptest.Server, path string, body interface{}, authKey string) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return postRaw(t, srv, path, data, authKey)
}

// postRaw sends a POST request with a raw body (no marshaling).
func postRaw(t *testing.T, srv *httptest.Server, path string, body []byte, authKey string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authKey != "" {
		req.Header.Set("Authorization", "Bearer "+authKey)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func getHTTP(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("get request: %v", err)
	}
	return resp
}

// ============================================================================
// HEALTH
// ============================================================================

func TestIntegration_Health(t *testing.T) {
	srv, _ := setup(t, "")
	resp := getHTTP(t, srv, "/health")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", body["status"])
	}
	if body["ready"] != true {
		t.Error("expected ready=true")
	}
	if _, ok := body["auth_mode"]; !ok {
		t.Error("health response missing auth_mode field")
	}
}

// ============================================================================
// INGEST → ACCUMULATE → REAL DATA TRANSITION
// ============================================================================

// TestIntegration_IngestAccumulatesAndTransitionToRealData verifies the core
// production flow: successive /ingest calls accumulate a time series per node,
// and once >= 4 samples are present the pipeline reports data_source="real".
func TestIntegration_IngestAccumulatesAndTransitionToRealData(t *testing.T) {
	srv, store := setup(t, "")

	type ingestBody struct {
		NodeID      string  `json:"node_id"`
		ArrivalRate float64 `json:"arrival_rate"`
		ServiceRate float64 `json:"service_rate"`
		QueueLength float64 `json:"queue_length"`
	}

	if store.HasRealData() {
		t.Fatal("store should not have real data before any ingestion")
	}

	// Ingest 5 samples to cross the >= 4 threshold.
	for i := 0; i < 5; i++ {
		body := ingestBody{
			NodeID:      "node_a",
			ArrivalRate: float64(5 + i),
			ServiceRate: 10.0,
			QueueLength: float64(i) * 0.5,
		}
		resp := postJSON(t, srv, "/ingest", body, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("ingest %d: expected 200, got %d", i, resp.StatusCode)
		}
	}

	if !store.HasRealData() {
		t.Fatal("store should have real data after 5 samples")
	}
	if store.SampleCount("node_a") != 5 {
		t.Errorf("expected 5 samples for node_a, got %d", store.SampleCount("node_a"))
	}

	// /analyze should now report data_source="real".
	body := ingestBody{NodeID: "node_a", ArrivalRate: 7.0, ServiceRate: 10.0, QueueLength: 1.5}
	resp := postJSON(t, srv, "/analyze", body, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("analyze: expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode analyze: %v", err)
	}
	if result["data_source"] != "real" {
		t.Errorf("expected data_source=real after ingestion, got %v", result["data_source"])
	}
}

// ============================================================================
// ANALYZE — SAFETY GATE FIELDS PRESENT
// ============================================================================

func TestIntegration_AnalyzeAlwaysIncludesSafetyGate(t *testing.T) {
	srv, store := setup(t, "")
	for i := 0; i < 5; i++ {
		store.Put("primary", metricsstore.NodeSample{ArrivalRate: 10.0, ServiceRate: 8.0, QueueLength: 5.0, Timestamp: float64(i * 8)})
	}

	body := map[string]interface{}{
		"arrival_rate": 10.0, "service_rate": 8.0, "queue_length": 5.0,
	}
	resp := postJSON(t, srv, "/analyze", body, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	safety, ok := result["safety"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing safety gate object")
	}

	for _, f := range []string{
		"confidence_score", "confidence_state", "latent_risk",
		"fallback_triggered", "posterior_variance", "posterior_precision", "determinism",
	} {
		if _, exists := safety[f]; !exists {
			t.Errorf("safety gate missing field: %s", f)
		}
	}

	if result["execution_time_ms"] == nil {
		t.Error("response missing execution_time_ms")
	}
}

// ============================================================================
// EXECUTION TIME POPULATED
// ============================================================================

func TestIntegration_ExecutionTimeMSIsPopulated(t *testing.T) {
	srv, store := setup(t, "")
	for i := 0; i < 5; i++ {
		store.Put("primary", metricsstore.NodeSample{ArrivalRate: 5.0, ServiceRate: 8.0, QueueLength: 1.0, Timestamp: float64(i * 8)})
	}

	body := map[string]interface{}{
		"arrival_rate": 5.0, "service_rate": 8.0, "queue_length": 1.0,
	}
	resp := postJSON(t, srv, "/analyze", body, "")
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	ms, ok := result["execution_time_ms"].(float64)
	if !ok {
		t.Fatal("execution_time_ms not a number")
	}
	if ms <= 0 {
		t.Errorf("expected execution_time_ms > 0, got %.1f", ms)
	}
}

// ============================================================================
// AUTHENTICATION — /act endpoint
// ============================================================================

func TestIntegration_ActRequiresBearerTokenWhenKeySet(t *testing.T) {
	const testKey = "test-secret-key-abc123"
	srv, store := setup(t, testKey)
	for i := 0; i < 5; i++ {
		store.Put("primary", metricsstore.NodeSample{ArrivalRate: 10.0, ServiceRate: 8.0, QueueLength: 5.0, Timestamp: float64(i * 8)})
	}

	body := map[string]interface{}{
		"arrival_rate": 10.0, "service_rate": 8.0, "queue_length": 5.0,
	}

	// Without token: 401.
	resp := postJSON(t, srv, "/act", body, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth token, got %d", resp.StatusCode)
	}

	// Wrong token: 401.
	resp2 := postJSON(t, srv, "/act", body, "wrong-token")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong token, got %d", resp2.StatusCode)
	}
}

func TestIntegration_ActAllowsRequestWithValidToken(t *testing.T) {
	const testKey = "correct-key-xyz"
	srv, store := setup(t, testKey)
	for i := 0; i < 5; i++ {
		store.Put("primary", metricsstore.NodeSample{ArrivalRate: 10.0, ServiceRate: 8.0, QueueLength: 5.0, Timestamp: float64(i * 8)})
	}

	body := map[string]interface{}{
		"arrival_rate": 10.0, "service_rate": 8.0, "queue_length": 5.0,
	}
	resp := postJSON(t, srv, "/act", body, testKey)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 200 or 503 with valid token, got %d", resp.StatusCode)
	}
}

func TestIntegration_ActPassthroughWhenNoKeyConfigured(t *testing.T) {
	srv, store := setup(t, "")
	for i := 0; i < 5; i++ {
		store.Put("primary", metricsstore.NodeSample{ArrivalRate: 10.0, ServiceRate: 8.0, QueueLength: 5.0, Timestamp: float64(i * 8)})
	}

	body := map[string]interface{}{
		"arrival_rate": 10.0, "service_rate": 8.0, "queue_length": 5.0,
	}
	resp := postJSON(t, srv, "/act", body, "")
	defer resp.Body.Close()
	// Pipeline may gate with 503 (safety UNKNOWN) or allow with 200.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 200 or 503 with auth disabled, got %d", resp.StatusCode)
	}
	// Must never crash (no panic), and response must be valid JSON.
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Errorf("response body is not valid JSON: %v", err)
	}
}

// ============================================================================
// REQUEST BODY SIZE LIMIT
// ============================================================================

func TestIntegration_OversizedBodyRejected(t *testing.T) {
	srv, _ := setup(t, "")
	api.SetMaxBodyBytes(100)
	t.Cleanup(func() { api.SetMaxBodyBytes(1 << 20) })

	// Build a valid-looking JSON body that exceeds 100 bytes.
	large := fmt.Sprintf(`{"arrival_rate":5.0,"service_rate":8.0,"queue_length":1.0,"node_id":"%s"}`,
		strings.Repeat("x", 200))

	resp := postRaw(t, srv, "/ingest", []byte(large), "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 413 or 400 for oversized body, got %d", resp.StatusCode)
	}
}

// ============================================================================
// METHOD ENFORCEMENT
// ============================================================================

func TestIntegration_GetToPostEndpointReturns405(t *testing.T) {
	srv, _ := setup(t, "")

	resp, err := srv.Client().Get(srv.URL + "/analyze")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET /analyze, got %d", resp.StatusCode)
	}
}

// ============================================================================
// PIPELINE CONCURRENCY
// ============================================================================

func TestIntegration_ConcurrentAnalyzeCalls(t *testing.T) {
	srv, store := setup(t, "")
	for i := 0; i < 5; i++ {
		store.Put("primary", metricsstore.NodeSample{ArrivalRate: 5.0, ServiceRate: 10.0, QueueLength: 1.0, Timestamp: float64(i * 8)})
	}

	const workers = 5
	done := make(chan error, workers)

	for i := 0; i < workers; i++ {
		go func(i int) {
			body := map[string]interface{}{
				"arrival_rate": float64(5 + i),
				"service_rate": 10.0,
				"queue_length": float64(i),
			}
			resp := postJSON(t, srv, "/analyze", body, "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				done <- fmt.Errorf("worker %d: expected 200, got %d", i, resp.StatusCode)
				return
			}
			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				done <- fmt.Errorf("worker %d: decode: %v", i, err)
				return
			}
			if result["safety"] == nil {
				done <- fmt.Errorf("worker %d: missing safety field", i)
				return
			}
			done <- nil
		}(i)
	}

	for i := 0; i < workers; i++ {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

// ============================================================================
// EXPLAIN — response structure
// ============================================================================

func TestIntegration_ExplainResponseStructure(t *testing.T) {
	srv, store := setup(t, "")
	for i := 0; i < 5; i++ {
		store.Put("primary", metricsstore.NodeSample{ArrivalRate: 8.0, ServiceRate: 6.0, QueueLength: 3.0, Timestamp: float64(i * 8)})
	}

	body := map[string]interface{}{
		"arrival_rate": 8.0, "service_rate": 6.0, "queue_length": 3.0,
	}
	resp := postJSON(t, srv, "/explain", body, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if result["success"] != true {
		t.Error("expected success=true")
	}
	if _, ok := result["safety"]; !ok {
		t.Error("response missing safety field")
	}
	if _, ok := result["execution_time_ms"]; !ok {
		t.Error("response missing execution_time_ms field")
	}
}

// ============================================================================
// CONTENT TYPE
// ============================================================================

func TestIntegration_AllEndpointsReturnJSON(t *testing.T) {
	srv, _ := setup(t, "")

	checks := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{"health_get", http.MethodGet, "/health", nil},
		{"ingest_valid", http.MethodPost, "/ingest", []byte(`{"arrival_rate":5,"service_rate":8,"queue_length":0}`)},
		{"ingest_invalid_json", http.MethodPost, "/ingest", []byte(`not-json`)},
		{"analyze_invalid_json", http.MethodPost, "/analyze", []byte(`{bad}`)},
	}

	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			var resp *http.Response
			if c.method == http.MethodGet {
				resp = getHTTP(t, srv, c.path)
			} else {
				resp = postRaw(t, srv, c.path, c.body, "")
			}
			defer resp.Body.Close()

			ct := resp.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				t.Errorf("%s %s: expected Content-Type application/json, got %q", c.method, c.path, ct)
			}
		})
	}
}

// ============================================================================
// INVALID JSON INPUT
// ============================================================================

func TestIntegration_InvalidJSONReturns400(t *testing.T) {
	srv, _ := setup(t, "")

	for _, path := range []string{"/ingest", "/analyze", "/explain"} {
		resp := postRaw(t, srv, path, []byte(`not-json`), "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s with invalid JSON: expected 400, got %d", path, resp.StatusCode)
		}
	}
}

func TestIntegration_RealTimeSignalStream(t *testing.T) {
	srv, _ := setup(t, "")

	type ingestBody struct {
		NodeID      string  `json:"node_id"`
		ArrivalRate float64 `json:"arrival_rate"`
		ServiceRate float64 `json:"service_rate"`
		QueueLength float64 `json:"queue_length"`
	}

	// simulate continuous stream
	for i := 0; i < 50; i++ {

		// simulate pattern (NOT random)
		body := ingestBody{
			NodeID:      "node_stream",
			ArrivalRate: 5 + float64(i%10), // wave pattern
			ServiceRate: 10.0,
			QueueLength: float64(i%5) * 1.5,
		}

		resp := postJSON(t, srv, "/ingest", body, "")
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("ingest failed at %d: %d", i, resp.StatusCode)
		}

		// every few steps → analyze
		if i > 0 && i%5 == 0 {
			resp := postJSON(t, srv, "/analyze", body, "")
			resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("analyze failed at %d: %d", i, resp.StatusCode)
			}
		}
	}
}

func TestIntegration_ParallelLoadBreakTest(t *testing.T) {
	srv, _ := setup(t, "")

	type ingestBody struct {
		NodeID      string  `json:"node_id"`
		ArrivalRate float64 `json:"arrival_rate"`
		ServiceRate float64 `json:"service_rate"`
		QueueLength float64 `json:"queue_length"`
	}

	const workers = 20
	done := make(chan error, workers)

	for w := 0; w < workers; w++ {
		go func(id int) {
			for i := 0; i < 30; i++ {

				// ✅ multi-node setup
				nodes := []string{"A", "B", "C"}
				node := nodes[i%3]

				body := ingestBody{
					NodeID:      node,
					ArrivalRate: 6,
					ServiceRate: 10.0,
					QueueLength: 2,
				}

				step := i % 20

				// B = root cause (strong, deterministic spike)
				if node == "B" && step > 5 {

					body.ArrivalRate = 15
				}

				// C = downstream effect (delayed, consistent)
				if node == "C" && step > 10 {
					body.QueueLength = 12
				}

				// A = effect node (no direct manipulation)

				resp := postJSON(t, srv, "/ingest", body, "")
				resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					done <- fmt.Errorf("worker %d ingest fail", id)
					return
				}

				if i >= 12 && i%3 == 0 {
					resp := postJSON(t, srv, "/analyze", body, "")
					resp.Body.Close()

					if resp.StatusCode != http.StatusOK {
						done <- fmt.Errorf("worker %d analyze fail", id)
						return
					}
				}
			}
			done <- nil
		}(w)
	}

	for i := 0; i < workers; i++ {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}
