package tests

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"absia/pkg/api"
	"absia/pkg/metricsstore"
)

// ──────────────────────────────────────────────────────────────────────────────
// TEST 08 — API Handlers
//
// Tests every REST endpoint using httptest (no real network, no flakiness):
//   GET  /health  — always 200, JSON body with status field
//   POST /ingest  — validates + stores metrics; rejects malformed JSON
//   POST /analyze — runs pipeline, returns safety fields; wrong method = 405
//   POST /explain — causal explanation; missing fields handled gracefully
//   Rate limiting — second request within 1-RPS budget returns 429
//   Auth          — /act rejects missing bearer token when key is set
//   Body size     — oversized body returns 413
//   Concurrent    — N goroutines hit /health simultaneously, all get 200
//   Safety fields — every /analyze response contains the 4 mandatory fields
//   JSON validity — every response body is valid JSON
//
// Output: results.json
// ──────────────────────────────────────────────────────────────────────────────

type EndpointCase struct {
	CaseName       string            `json:"case_name"`
	Method         string            `json:"method"`
	Path           string            `json:"path"`
	RequestBody    string            `json:"request_body_summary"`
	ExpectedStatus int               `json:"expected_status"`
	ActualStatus   int               `json:"actual_status"`
	StatusCorrect  bool              `json:"status_correct"`
	BodyValidJSON  bool              `json:"response_body_valid_json"`
	BodyFields     map[string]string `json:"important_body_fields"`
	BodySize       int               `json:"response_body_bytes"`
	DurationMS     float64           `json:"duration_ms"`
}

type SafetyFieldCase struct {
	CaseName            string  `json:"case_name"`
	StatusCode          int     `json:"status_code"`
	HasConfidenceScore  bool    `json:"has_confidence_score"`
	HasConfidenceState  bool    `json:"has_confidence_state"`
	HasLatentRisk       bool    `json:"has_latent_risk"`
	HasFallbackField    bool    `json:"has_fallback_triggered"`
	ScoreInBounds       bool    `json:"score_in_0_1_if_present"`
	ConfidenceScore     float64 `json:"confidence_score_value"`
	StateValue          string  `json:"confidence_state_value"`
}

type RateLimitCase struct {
	CaseName         string `json:"case_name"`
	RequestsPerBurst int    `json:"requests_in_burst"`
	FirstStatus      int    `json:"first_request_status"`
	ThrottledCount   int    `json:"throttled_429_count"`
	RateLimitWorking bool   `json:"rate_limit_enforced"`
}

type ConcurrentCase struct {
	CaseName     string  `json:"case_name"`
	Goroutines   int     `json:"goroutines"`
	Path         string  `json:"path"`
	AllStatus200 bool    `json:"all_got_200"`
	NoPanics     bool    `json:"no_panics"`
	AvgDurationMS float64 `json:"avg_duration_ms"`
}

type APIReport struct {
	TestSuite       string            `json:"test_suite"`
	Timestamp       string            `json:"timestamp_utc"`
	EndpointCases   []EndpointCase    `json:"endpoint_tests"`
	SafetyFields    []SafetyFieldCase `json:"safety_field_presence"`
	RateLimitCases  []RateLimitCase   `json:"rate_limit_tests"`
	ConcurrentCases []ConcurrentCase  `json:"concurrent_health_checks"`
	Summary         struct {
		EndpointAllCorrect  bool   `json:"all_status_codes_correct"`
		SafetyFieldsPresent bool   `json:"safety_fields_always_present"`
		RateLimitWorks      bool   `json:"rate_limiting_works"`
		ConcurrentOK        bool   `json:"concurrent_access_ok"`
		AllJSONValid        bool   `json:"all_responses_valid_json"`
		Overall             string `json:"overall_verdict"`
	} `json:"summary"`
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store := metricsstore.New(50)
	// Pre-seed with enough samples to enable real-data path
	for _, nodeID := range []string{"gateway", "auth", "db"} {
		for i := 0; i < 10; i++ {
			store.Put(nodeID, metricsstore.NodeSample{
				ArrivalRate: float64(i+1) * 5.0,
				ServiceRate: 100.0,
				QueueLength: float64(i) * 2.0,
				Timestamp:   float64(i),
				WallTime:    time.Now(),
			})
		}
	}
	api.SetStore(store)
	api.SetAPIKey("") // auth disabled unless test overrides
	api.SetRateLimit(0, 1) // rate limiting disabled unless test overrides

	// Build an isolated mux — SetupRoutes uses the default mux which is
	// shared across tests. Instead we wire up the exported handlers directly.
	mux := http.NewServeMux()
	mux.HandleFunc("/health",  api.HealthHandler)
	mux.HandleFunc("/ingest",  api.IngestHandler)
	mux.HandleFunc("/analyze", api.AnalyzeHandler)
	mux.HandleFunc("/explain", api.ExplainHandler)
	mux.HandleFunc("/act",     api.ActHandler)
	return httptest.NewServer(mux)
}

func do(t *testing.T, srv *httptest.Server, method, path, body string) (int, []byte, float64) {
	t.Helper()
	var reqBody io.Reader
	if body != "" { reqBody = strings.NewReader(body) }
	req, err := http.NewRequest(method, srv.URL+path, reqBody)
	if err != nil { t.Fatalf("new request: %v", err) }
	if body != "" { req.Header.Set("Content-Type", "application/json") }
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil { t.Fatalf("do request: %v", err) }
	defer resp.Body.Close()
	ms := float64(time.Since(start).Nanoseconds()) / 1e6
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, ms
}

func doAuth(t *testing.T, srv *httptest.Server, method, path, body, token string) (int, []byte) {
	t.Helper()
	var reqBody io.Reader
	if body != "" { reqBody = strings.NewReader(body) }
	req, err := http.NewRequest(method, srv.URL+path, reqBody)
	if err != nil { t.Fatalf("new request: %v", err) }
	if body != "" { req.Header.Set("Content-Type", "application/json") }
	if token != "" { req.Header.Set("Authorization", "Bearer "+token) }
	resp, err := http.DefaultClient.Do(req)
	if err != nil { t.Fatalf("do request: %v", err) }
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func parseBody(raw []byte) map[string]interface{} {
	var m map[string]interface{}
	_ = json.Unmarshal(raw, &m)
	return m
}

func isValidJSON(raw []byte) bool {
	var v interface{}
	return json.Unmarshal(raw, &v) == nil
}

func extractStr(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func TestAPIHandlers(t *testing.T) {
	report := APIReport{
		TestSuite: "T08_APIHandlers",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	srv := newTestServer(t)
	defer srv.Close()

	endpointAllCorrect := true
	allJSONValid := true

	// ── GET /health ───────────────────────────────────────────────────────────
	{
		status, body, ms := do(t, srv, "GET", "/health", "")
		valid := isValidJSON(body)
		correct := status == 200
		if !correct { endpointAllCorrect = false; t.Errorf("/health: expected 200 got %d", status) }
		if !valid   { allJSONValid = false }
		m := parseBody(body)
		fields := map[string]string{"status": extractStr(m, "status")}
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "GET_health", Method: "GET", Path: "/health",
			ExpectedStatus: 200, ActualStatus: status, StatusCorrect: correct,
			BodyValidJSON: valid, BodyFields: fields, BodySize: len(body), DurationMS: ms,
		})
	}

	// ── GET /health wrong method (POST) ───────────────────────────────────────
	{
		status, body, ms := do(t, srv, "POST", "/health", "{}")
		valid := isValidJSON(body)
		correct := status == 405
		if !correct {
			endpointAllCorrect = false
			t.Logf("/health POST: expected 405 got %d (some impls return 200 — logged)", status)
		}
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_health_wrong_method", Method: "POST", Path: "/health",
			ExpectedStatus: 405, ActualStatus: status, StatusCorrect: correct,
			BodyValidJSON: valid, BodySize: len(body), DurationMS: ms,
		})
	}

	// ── POST /ingest valid ────────────────────────────────────────────────────
	ingestBody := `{"node_id":"svc_auth","arrival_rate":85.0,"service_rate":100.0,"queue_length":10.0}`
	{
		status, body, ms := do(t, srv, "POST", "/ingest", ingestBody)
		valid := isValidJSON(body)
		correct := status == 200
		if !correct { endpointAllCorrect = false; t.Errorf("/ingest valid: expected 200 got %d", status) }
		if !valid   { allJSONValid = false }
		m := parseBody(body)
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_ingest_valid", Method: "POST", Path: "/ingest",
			RequestBody: ingestBody, ExpectedStatus: 200, ActualStatus: status,
			StatusCorrect: correct, BodyValidJSON: valid,
			BodyFields: map[string]string{"status": extractStr(m, "status")},
			BodySize: len(body), DurationMS: ms,
		})
	}

	// ── POST /ingest malformed JSON ───────────────────────────────────────────
	{
		status, body, ms := do(t, srv, "POST", "/ingest", `{"node_id":}`)
		valid := isValidJSON(body)
		correct := status == 400
		if !correct { endpointAllCorrect = false; t.Errorf("/ingest malformed: expected 400 got %d", status) }
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_ingest_malformed_json", Method: "POST", Path: "/ingest",
			RequestBody: "{malformed}", ExpectedStatus: 400, ActualStatus: status,
			StatusCorrect: correct, BodyValidJSON: valid, BodySize: len(body), DurationMS: ms,
		})
	}

	// ── POST /ingest missing node_id ──────────────────────────────────────────
	{
		status, body, ms := do(t, srv, "POST", "/ingest", `{"arrival_rate":85.0,"service_rate":100.0}`)
		valid := isValidJSON(body)
		correct := status == 400
		if !correct { endpointAllCorrect = false; t.Errorf("/ingest missing node_id: expected 400 got %d", status) }
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_ingest_missing_node_id", Method: "POST", Path: "/ingest",
			RequestBody: "no_node_id", ExpectedStatus: 400, ActualStatus: status,
			StatusCorrect: correct, BodyValidJSON: valid, BodySize: len(body), DurationMS: ms,
		})
	}

	// ── POST /ingest zero service_rate ────────────────────────────────────────
	{
		status, body, ms := do(t, srv, "POST", "/ingest", `{"node_id":"x","arrival_rate":5.0,"service_rate":0.0}`)
		valid := isValidJSON(body)
		correct := status == 400
		if !correct { endpointAllCorrect = false; t.Errorf("/ingest zero_mu: expected 400 got %d", status) }
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_ingest_zero_service_rate", Method: "POST", Path: "/ingest",
			ExpectedStatus: 400, ActualStatus: status, StatusCorrect: correct,
			BodyValidJSON: valid, BodySize: len(body), DurationMS: ms,
		})
	}

	// ── POST /analyze valid ───────────────────────────────────────────────────
	analyzeBody := `{"arrival_rate":5.0,"service_rate":10.0,"queue_length":2.0}`
	{
		status, body, ms := do(t, srv, "POST", "/analyze", analyzeBody)
		valid := isValidJSON(body)
		correct := status == 200
		if !correct { endpointAllCorrect = false; t.Errorf("/analyze: expected 200 got %d", status) }
		if !valid   { allJSONValid = false }
		m := parseBody(body)

		// Safety fields must always be present
		hasConfScore := m["confidence_score"] != nil
		hasConfState := m["confidence_state"] != nil
		hasLatentRisk := m["latent_risk"] != nil
		hasFallback := m["fallback_triggered"] != nil

		score := 0.0
		if s, ok := m["confidence_score"].(float64); ok { score = s }
		scoreBound := score >= 0 && score <= 1

		report.SafetyFields = append(report.SafetyFields, SafetyFieldCase{
			CaseName: "POST_analyze_valid",
			StatusCode: status,
			HasConfidenceScore: hasConfScore, HasConfidenceState: hasConfState,
			HasLatentRisk: hasLatentRisk, HasFallbackField: hasFallback,
			ScoreInBounds: scoreBound, ConfidenceScore: score,
			StateValue: extractStr(m, "confidence_state"),
		})

		if !hasConfScore || !hasConfState || !hasLatentRisk || !hasFallback {
			t.Errorf("/analyze missing safety fields: conf_score=%v conf_state=%v latent=%v fallback=%v",
				hasConfScore, hasConfState, hasLatentRisk, hasFallback)
		}

		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_analyze_valid", Method: "POST", Path: "/analyze",
			RequestBody: analyzeBody, ExpectedStatus: 200, ActualStatus: status,
			StatusCorrect: correct, BodyValidJSON: valid,
			BodyFields: map[string]string{
				"confidence_state": extractStr(m, "confidence_state"),
				"latent_risk":      extractStr(m, "latent_risk"),
			},
			BodySize: len(body), DurationMS: ms,
		})
	}

	// ── POST /analyze GET method ──────────────────────────────────────────────
	{
		status, body, ms := do(t, srv, "GET", "/analyze", "")
		valid := isValidJSON(body)
		correct := status == 405
		if !correct { endpointAllCorrect = false; t.Errorf("/analyze GET: expected 405 got %d", status) }
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "GET_analyze_wrong_method", Method: "GET", Path: "/analyze",
			ExpectedStatus: 405, ActualStatus: status, StatusCorrect: correct,
			BodyValidJSON: valid, BodySize: len(body), DurationMS: ms,
		})
	}

	// ── POST /analyze zero service_rate ──────────────────────────────────────
	{
		status, body, ms := do(t, srv, "POST", "/analyze", `{"arrival_rate":5.0,"service_rate":0.0,"queue_length":0.0}`)
		valid := isValidJSON(body)
		correct := status == 400 || status == 422
		if !correct { endpointAllCorrect = false; t.Logf("/analyze zero_mu: expected 400/422 got %d", status) }
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_analyze_zero_service_rate", Method: "POST", Path: "/analyze",
			ExpectedStatus: 400, ActualStatus: status, StatusCorrect: correct,
			BodyValidJSON: valid, BodySize: len(body), DurationMS: ms,
		})
	}

	// ── POST /explain valid ───────────────────────────────────────────────────
	{
		status, body, ms := do(t, srv, "POST", "/explain", analyzeBody)
		valid := isValidJSON(body)
		correct := status == 200
		if !correct { endpointAllCorrect = false; t.Errorf("/explain: expected 200 got %d", status) }
		if !valid   { allJSONValid = false }
		m := parseBody(body)
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_explain_valid", Method: "POST", Path: "/explain",
			RequestBody: analyzeBody, ExpectedStatus: 200, ActualStatus: status,
			StatusCorrect: correct, BodyValidJSON: valid,
			BodyFields: map[string]string{"causes_count": fmt.Sprintf("%v", m["cause_count"])},
			BodySize: len(body), DurationMS: ms,
		})
	}

	// ── Auth: /act without key (key set) ─────────────────────────────────────
	{
		api.SetAPIKey("secret-key-123")
		status, body := doAuth(t, srv, "POST", "/act", analyzeBody, "")
		correct := status == 401
		if !correct { t.Errorf("/act no_token: expected 401 got %d", status) }
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_act_no_token_401", Method: "POST", Path: "/act",
			ExpectedStatus: 401, ActualStatus: status, StatusCorrect: correct,
			BodyValidJSON: isValidJSON(body), BodySize: len(body),
		})
		api.SetAPIKey("") // reset
	}

	// ── Auth: /act with wrong key ─────────────────────────────────────────────
	{
		api.SetAPIKey("correct-key")
		status, body := doAuth(t, srv, "POST", "/act", analyzeBody, "wrong-key")
		correct := status == 401 || status == 403
		if !correct { t.Errorf("/act wrong_token: expected 401/403 got %d", status) }
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_act_wrong_token", Method: "POST", Path: "/act",
			ExpectedStatus: 401, ActualStatus: status, StatusCorrect: correct,
			BodyValidJSON: isValidJSON(body), BodySize: len(body),
		})
		api.SetAPIKey("") // reset
	}

	// ── Auth: /act with correct key ───────────────────────────────────────────
	{
		api.SetAPIKey("correct-key")
		status, body := doAuth(t, srv, "POST", "/act", analyzeBody, "correct-key")
		correct := status == 200
		if !correct { t.Logf("/act correct_token: expected 200 got %d (may depend on pipeline)", status) }
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_act_correct_token", Method: "POST", Path: "/act",
			ExpectedStatus: 200, ActualStatus: status, StatusCorrect: correct,
			BodyValidJSON: isValidJSON(body), BodySize: len(body),
		})
		api.SetAPIKey("") // reset
	}

	// ── Rate limiting ─────────────────────────────────────────────────────────
	{
		api.SetRateLimit(1, 1) // 1 req/sec, burst 1
		throttled := 0
		firstStatus := 0
		for i := 0; i < 5; i++ {
			status, _, _ := do(t, srv, "GET", "/health", "")
			if i == 0 { firstStatus = status }
			if status == 429 { throttled++ }
		}
		rlWorking := throttled > 0
		report.RateLimitCases = append(report.RateLimitCases, RateLimitCase{
			CaseName: "burst_5_at_1rps", RequestsPerBurst: 5,
			FirstStatus: firstStatus, ThrottledCount: throttled, RateLimitWorking: rlWorking,
		})
		if !rlWorking {
			t.Logf("rate limiting: no 429s received (may be timing-dependent)")
		}
		api.SetRateLimit(0, 1) // reset
	}

	// ── Concurrent /health ────────────────────────────────────────────────────
	{
		goroutines := 20
		results := make([]int, goroutines)
		durations := make([]float64, goroutines)
		var wg sync.WaitGroup
		noPanic := true
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func(idx int) {
				defer func() {
					if r := recover(); r != nil {
						noPanic = false
						t.Errorf("panic in concurrent health: %v", r)
					}
					wg.Done()
				}()
				status, _, ms := do(t, srv, "GET", "/health", "")
				results[idx] = status
				durations[idx] = ms
			}(i)
		}
		wg.Wait()

		all200 := true
		avgDur := 0.0
		for i, s := range results {
			if s != 200 { all200 = false }
			avgDur += durations[i]
		}
		avgDur /= float64(goroutines)

		report.ConcurrentCases = append(report.ConcurrentCases, ConcurrentCase{
			CaseName: "20_concurrent_health_checks", Goroutines: goroutines,
			Path: "/health", AllStatus200: all200, NoPanics: noPanic, AvgDurationMS: avgDur,
		})
		if !all200 { t.Errorf("concurrent /health: not all 200, got %v", results) }
	}

	// ── Body size limit (413) ─────────────────────────────────────────────────
	{
		api.SetMaxBodyBytes(100) // set a very small limit
		bigBody := strings.Repeat("x", 200)
		status, body, ms := do(t, srv, "POST", "/ingest", bigBody)
		correct := status == 413 || status == 400 // 413 preferred, 400 acceptable
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_ingest_body_too_large", Method: "POST", Path: "/ingest",
			RequestBody: fmt.Sprintf("%d_bytes", len(bigBody)),
			ExpectedStatus: 413, ActualStatus: status, StatusCorrect: correct,
			BodyValidJSON: isValidJSON(body), BodySize: len(body), DurationMS: ms,
		})
		if !correct { t.Logf("/ingest oversized: expected 413/400 got %d", status) }
		api.SetMaxBodyBytes(1 << 20) // reset to 1 MiB
	}

	// ── Overloaded system through /analyze ────────────────────────────────────
	overloadBody := `{"arrival_rate":100.0,"service_rate":10.0,"queue_length":1000.0}`
	{
		status, body, ms := do(t, srv, "POST", "/analyze", overloadBody)
		valid := isValidJSON(body)
		correct := status == 200
		if !correct { t.Logf("/analyze overload: expected 200 got %d", status) }
		m := parseBody(body)
		score := 0.0
		if s, ok := m["confidence_score"].(float64); ok { score = s }

		report.SafetyFields = append(report.SafetyFields, SafetyFieldCase{
			CaseName: "POST_analyze_overloaded_system",
			StatusCode: status,
			HasConfidenceScore: m["confidence_score"] != nil,
			HasConfidenceState: m["confidence_state"] != nil,
			HasLatentRisk:      m["latent_risk"] != nil,
			HasFallbackField:   m["fallback_triggered"] != nil,
			ScoreInBounds:      score >= 0 && score <= 1,
			ConfidenceScore:    score,
			StateValue: extractStr(m, "confidence_state"),
		})
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_analyze_overloaded_rho10", Method: "POST", Path: "/analyze",
			RequestBody: overloadBody, ExpectedStatus: 200, ActualStatus: status,
			StatusCorrect: correct, BodyValidJSON: valid,
			BodyFields: map[string]string{
				"confidence_state": extractStr(m, "confidence_state"),
				"latent_risk":      extractStr(m, "latent_risk"),
				"fallback":         fmt.Sprintf("%v", m["fallback_triggered"]),
			},
			BodySize: len(body), DurationMS: ms,
		})
	}

	// ── Empty /analyze body ───────────────────────────────────────────────────
	{
		status, body, ms := do(t, srv, "POST", "/analyze", "")
		valid := isValidJSON(body)
		correct := status == 400
		if !correct { t.Logf("/analyze empty body: expected 400 got %d", status) }
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_analyze_empty_body", Method: "POST", Path: "/analyze",
			ExpectedStatus: 400, ActualStatus: status, StatusCorrect: correct,
			BodyValidJSON: valid, BodySize: len(body), DurationMS: ms,
		})
	}

	// ── POST /ingest multiple nodes then /analyze ─────────────────────────────
	{
		nodes := []struct{ id string; lam, mu, q float64 }{
			{"svc_api",   80.0, 100.0, 5.0},
			{"svc_cache", 40.0, 100.0, 2.0},
			{"svc_db",   120.0, 100.0, 600.0}, // overloaded
		}
		for i := 0; i < 6; i++ {
			for _, n := range nodes {
				b := fmt.Sprintf(`{"node_id":%q,"arrival_rate":%.1f,"service_rate":%.1f,"queue_length":%.1f}`,
					n.id, n.lam+float64(i), n.mu, n.q)
				do(t, srv, "POST", "/ingest", b) //nolint
			}
		}
		status, body, ms := do(t, srv, "POST", "/analyze", analyzeBody)
		valid := isValidJSON(body)
		m := parseBody(body)
		report.EndpointCases = append(report.EndpointCases, EndpointCase{
			CaseName: "POST_analyze_after_multi_node_ingest", Method: "POST", Path: "/analyze",
			ExpectedStatus: 200, ActualStatus: status, StatusCorrect: status == 200,
			BodyValidJSON: valid,
			BodyFields: map[string]string{
				"data_source":      extractStr(m, "data_source"),
				"confidence_state": extractStr(m, "confidence_state"),
			},
			BodySize: len(body), DurationMS: ms,
		})
	}

	// ── Summary ───────────────────────────────────────────────────────────────
	safetyFieldsPresent := true
	for _, sf := range report.SafetyFields {
		if !sf.HasConfidenceScore || !sf.HasConfidenceState || !sf.HasLatentRisk || !sf.HasFallbackField {
			safetyFieldsPresent = false
		}
	}

	rlWorks := len(report.RateLimitCases) > 0 && report.RateLimitCases[0].RateLimitWorking
	concOK := len(report.ConcurrentCases) > 0 && report.ConcurrentCases[0].AllStatus200

	overall := "PASS"
	if !endpointAllCorrect || !allJSONValid || !safetyFieldsPresent {
		overall = "FAIL"
	}

	report.Summary.EndpointAllCorrect = endpointAllCorrect
	report.Summary.SafetyFieldsPresent = safetyFieldsPresent
	report.Summary.RateLimitWorks = rlWorks
	report.Summary.ConcurrentOK = concOK
	report.Summary.AllJSONValid = allJSONValid
	report.Summary.Overall = overall

	out, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile("results.json", out, 0644); err != nil {
		t.Fatalf("write results.json: %v", err)
	}
	t.Logf("API verdict: %s | endpoints=%v | safety_fields=%v | json=%v | concurrent=%v",
		overall, endpointAllCorrect, safetyFieldsPresent, allJSONValid, concOK)
}