package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"absia/pkg/metricsstore"
)

func floatPtr(v float64) *float64 {
	return &v
}

func TestHealthHandler_OK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	HealthHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	var resp HealthResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("cannot decode health response: %v", err)
	}
	if !resp.Ready {
		t.Error("health response: Ready should be true")
	}
}

//
// INPUT VALIDATION — MetricsRequest
//

func TestMetricsRequest_ValidInput(t *testing.T) {
	req := MetricsRequest{ArrivalRate: floatPtr(5.0), ServiceRate: floatPtr(8.0), QueueLength: floatPtr(2.0)}
	errs := req.validate()
	if len(errs) != 0 {
		t.Errorf("valid input produced validation errors: %v", errs)
	}
}

func TestMetricsRequest_ZeroServiceRate(t *testing.T) {
	req := MetricsRequest{ArrivalRate: floatPtr(5.0), ServiceRate: floatPtr(0.0), QueueLength: floatPtr(0.0)}
	errs := req.validate()
	if len(errs) == 0 {
		t.Error("zero service_rate should fail validation")
	}
	if !containsSubstring(errs, "service_rate") {
		t.Errorf("error should mention service_rate, got: %v", errs)
	}
}

func TestMetricsRequest_NegativeServiceRate(t *testing.T) {
	req := MetricsRequest{ArrivalRate: floatPtr(5.0), ServiceRate: floatPtr(-1.0), QueueLength: floatPtr(0.0)}
	errs := req.validate()
	if len(errs) == 0 {
		t.Error("negative service_rate should fail validation")
	}
}

func TestMetricsRequest_NegativeArrivalRate(t *testing.T) {
	req := MetricsRequest{ArrivalRate: floatPtr(-0.1), ServiceRate: floatPtr(8.0), QueueLength: floatPtr(0.0)}
	errs := req.validate()
	if len(errs) == 0 {
		t.Error("negative arrival_rate should fail validation")
	}
}

func TestMetricsRequest_NegativeQueueLength(t *testing.T) {
	req := MetricsRequest{ArrivalRate: floatPtr(5.0), ServiceRate: floatPtr(8.0), QueueLength: floatPtr(-1.0)}
	errs := req.validate()
	if len(errs) == 0 {
		t.Error("negative queue_length should fail validation")
	}
}

func TestMetricsRequest_ExcessiveArrivalRate(t *testing.T) {
	req := MetricsRequest{ArrivalRate: floatPtr(2e9), ServiceRate: floatPtr(8.0), QueueLength: floatPtr(0.0)}
	errs := req.validate()
	if len(errs) == 0 {
		t.Error("arrival_rate > 1e9 should fail validation")
	}
}

func TestMetricsRequest_ExcessiveServiceRate(t *testing.T) {
	req := MetricsRequest{ArrivalRate: floatPtr(5.0), ServiceRate: floatPtr(2e9), QueueLength: floatPtr(0.0)}
	errs := req.validate()
	if len(errs) == 0 {
		t.Error("service_rate > 1e9 should fail validation")
	}
}

func TestMetricsRequest_ZeroArrivalRateIsValid(t *testing.T) {
	// arrivalRate=0 is valid (idle system).
	req := MetricsRequest{ArrivalRate: floatPtr(0.0), ServiceRate: floatPtr(8.0), QueueLength: floatPtr(0.0)}
	errs := req.validate()
	if len(errs) != 0 {
		t.Errorf("zero arrival_rate should be valid (idle system), got errors: %v", errs)
	}
}

//
// METHOD ENFORCEMENT
//

func TestIngestHandler_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ingest", nil)
	rr := httptest.NewRecorder()
	IngestHandler(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /ingest should return 405, got %d", rr.Code)
	}
}

func TestAnalyzeHandler_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/analyze", nil)
	rr := httptest.NewRecorder()
	AnalyzeHandler(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /analyze should return 405, got %d", rr.Code)
	}
}

//
// BODY DECODING — INVALID JSON
//

func TestIngestHandler_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader("not-json"))
	rr := httptest.NewRecorder()
	IngestHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON should return 400, got %d", rr.Code)
	}
	assertErrorResponse(t, rr)
}

func TestAnalyzeHandler_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/analyze", strings.NewReader("{bad}"))
	rr := httptest.NewRecorder()
	AnalyzeHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON should return 400, got %d", rr.Code)
	}
}

// BUSINESS VALIDATION — ZERO SERVICE RATE VIA HTTP
//

func TestAnalyzeHandler_ZeroServiceRateReturns400(t *testing.T) {
	body := `{"arrival_rate":10,"service_rate":0,"queue_length":5}`
	req := httptest.NewRequest(http.MethodPost, "/analyze", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	AnalyzeHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("service_rate=0 should return 400, got %d", rr.Code)
	}
	assertErrorResponse(t, rr)
}

func TestIngestHandler_ZeroServiceRateReturns400(t *testing.T) {
	body := `{"arrival_rate":5,"service_rate":0,"queue_length":0}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	IngestHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("service_rate=0 should return 400, got %d", rr.Code)
	}
}

//
// INGEST — HAPPY PATH
//

func TestIngestHandler_ValidInput(t *testing.T) {
	body := `{"node_id":"api","arrival_rate":5.0,"service_rate":8.0,"queue_length":2.0}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	IngestHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("valid ingest should return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	assertContentTypeJSON(t, rr)
}

func TestNodesHandler_ReturnsStoreInventory(t *testing.T) {
	store := metricsstore.New(10)
	store.Put("api", metricsstore.NodeSample{
		ArrivalRate: 50,
		ServiceRate: 100,
		QueueLength: 4,
		Timestamp:   float64(time.Now().Unix()),
		WallTime:    time.Now(),
	})
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })

	req := httptest.NewRequest(http.MethodGet, "/nodes", nil)
	rr := httptest.NewRecorder()
	NodesHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("nodes should return 200, got %d", rr.Code)
	}
	assertContentTypeJSON(t, rr)

	var resp NodesResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("cannot decode nodes response: %v", err)
	}
	if !resp.Success {
		t.Error("nodes response should have success=true")
	}
	if resp.NodeCount != 1 || len(resp.Nodes) != 1 {
		t.Fatalf("expected one node, got count=%d len=%d", resp.NodeCount, len(resp.Nodes))
	}
	if resp.Nodes[0].NodeID != "api" {
		t.Errorf("expected api node, got %q", resp.Nodes[0].NodeID)
	}
	if resp.Nodes[0].Load != 0.5 {
		t.Errorf("expected load=0.5, got %f", resp.Nodes[0].Load)
	}
}

//
// ANALYZE — HAPPY PATH (STABLE + OVERLOADED)
//

func TestAnalyzeHandler_StableSystem(t *testing.T) {
	s := metricsstore.New(10)
	for i := 0; i < 5; i++ {
		s.Put("primary", metricsstore.NodeSample{ArrivalRate: 3.0, ServiceRate: 10.0, QueueLength: 0.5, Timestamp: float64(i * 8)})
	}
	SetStore(s)
	t.Cleanup(func() { SetStore(nil) })

	body := `{"arrival_rate":3.0,"service_rate":10.0,"queue_length":0.5}`
	req := httptest.NewRequest(http.MethodPost, "/analyze", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	AnalyzeHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("stable system should return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	assertContentTypeJSON(t, rr)
	var resp AnalysisResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("cannot decode analysis response: %v", err)
	}
	if !resp.Success {
		t.Error("stable system should produce success=true")
	}
}

func TestAnalyzeHandler_OverloadedSystem(t *testing.T) {
	s := metricsstore.New(10)
	for i := 0; i < 5; i++ {
		s.Put("primary", metricsstore.NodeSample{ArrivalRate: 10.0, ServiceRate: 8.0, QueueLength: 5.0, Timestamp: float64(i * 8)})
	}
	SetStore(s)
	t.Cleanup(func() { SetStore(nil) })

	body := `{"arrival_rate":10.0,"service_rate":8.0,"queue_length":5.0}`
	req := httptest.NewRequest(http.MethodPost, "/analyze", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	AnalyzeHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("overloaded system should return 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

//
// CONTENT TYPE
//

func TestHandlers_ContentTypeAlwaysJSON(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		path    string
		handler http.HandlerFunc
		body    string
	}{
		{"health", http.MethodGet, "/health", HealthHandler, ""},
		{"ingest_valid", http.MethodPost, "/ingest",
			IngestHandler, `{"arrival_rate":5,"service_rate":8,"queue_length":0}`},
		{"ingest_invalid", http.MethodPost, "/ingest", IngestHandler, "bad"},
		{"analyze_invalid", http.MethodPost, "/analyze", AnalyzeHandler, "bad"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rr := httptest.NewRecorder()
			tc.handler(rr, req)
			assertContentTypeJSON(t, rr)
		})
	}
}

//
// HELPERS
//

func assertContentTypeJSON(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type: application/json, got %q", ct)
	}
}

func assertErrorResponse(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	var resp ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("cannot decode error response: %v", err)
	}
	if resp.Success {
		t.Error("error response should have success=false")
	}
	if len(resp.Errors) == 0 {
		t.Error("error response should have at least one error message")
	}
}

func containsSubstring(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
