package receivers_test

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"absia/pkg/metricsstore"
	"absia/pkg/receivers"
)

func TestOTLPReceiver(t *testing.T) {
	store := metricsstore.New(20)
	receiver := receivers.NewOTLPReceiver(store, nil)

	jsonPayload := []byte(`{
		"resourceMetrics": [
			{
				"resource": {
					"attributes": [
						{ "key": "service.name", "value": { "stringValue": "test-service" } }
					]
				},
				"scopeMetrics": [
					{
						"metrics": [
							{
								"name": "requests_per_second",
								"gauge": { "dataPoints": [ { "asDouble": 150.5 } ] }
							},
							{
								"name": "p50_latency_ms",
								"gauge": { "dataPoints": [ { "asDouble": 50.0 } ] }
							}
						]
					}
				]
			}
		]
	}`)

	port := 4319
	receiver.Start(port)
	time.Sleep(200 * time.Millisecond) // Wait for server to start

	client := &http.Client{}
	resp, err := client.Post("http://localhost:4319/v1/metrics", "application/json", bytes.NewReader(jsonPayload))
	if err != nil {
		t.Fatalf("Failed to POST metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond) // Let store put complete

	sample, ok := store.GetLatestSample("test-service")
	if !ok {
		t.Fatal("Expected test-service to have a sample")
	}

	if sample.MetricSource != "otel" {
		t.Errorf("Expected source otel, got %s", sample.MetricSource)
	}
	if sample.ArrivalRate != 150.5 {
		t.Errorf("Expected ArrivalRate 150.5, got %v", sample.ArrivalRate)
	}
	if sample.ServiceRate != 20.0 { // 1000 / 50.0
		t.Errorf("Expected ServiceRate 20.0, got %v", sample.ServiceRate)
	}
}
