package receivers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"absia/pkg/metricsstore"
)

// OTLPReceiver handles incoming OTLP metrics in JSON format.
type OTLPReceiver struct {
	store *metricsstore.Store
}

// NewOTLPReceiver creates a new receiver instance.
func NewOTLPReceiver(store *metricsstore.Store) *OTLPReceiver {
	return &OTLPReceiver{store: store}
}

// Start HTTP server for OTLP metrics on the given port (default 4318).
func (r *OTLPReceiver) Start(port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/metrics", r.handleMetrics)
	
	addr := fmt.Sprintf(":%d", port)
	log.Printf("[OTLP] Starting OTLP metric receiver on %s", addr)
	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("[OTLP] Receiver server failed: %v", err)
		}
	}()
}

func (r *OTLPReceiver) handleMetrics(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	// Lightweight parsing for OTLP JSON metric payload
	var payload struct {
		ResourceMetrics []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeMetrics []struct {
				Metrics []struct {
					Name  string `json:"name"`
					Gauge struct {
						DataPoints []struct {
							AsDouble float64 `json:"asDouble"`
							AsInt    int64   `json:"asInt"`
						} `json:"dataPoints"`
					} `json:"gauge"`
					Sum struct {
						DataPoints []struct {
							AsDouble float64 `json:"asDouble"`
							AsInt    int64   `json:"asInt"`
						} `json:"dataPoints"`
					} `json:"sum"`
				} `json:"metrics"`
			} `json:"scopeMetrics"`
		} `json:"resourceMetrics"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "Invalid OTLP JSON", http.StatusBadRequest)
		return
	}

	for _, rm := range payload.ResourceMetrics {
		nodeID := "unknown"
		for _, attr := range rm.Resource.Attributes {
			if attr.Key == "service.name" {
				nodeID = attr.Value.StringValue
				break
			}
		}

		sample := metricsstore.NodeSample{
			Timestamp:     float64(time.Now().Unix()),
			WallTime:      time.Now(),
			MetricSource:  "otel",
			MetricQuality: 0.9, // High quality for OTLP
		}

		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				val := extractMetricValue(m.Gauge.DataPoints, m.Sum.DataPoints)
				
				switch m.Name {
				case "requests_per_second":
					sample.ArrivalRate = val
				case "p50_latency_ms":
					if val > 0 {
						sample.ServiceRate = 1000.0 / val // 1 / latency in seconds
					}
				case "requests_in_flight":
					sample.QueueLength = val
				case "cpu_utilization":
					sample.ComputePressure = val
				case "memory_utilization":
					sample.MemoryPressure = val
				case "network_saturation":
					sample.NetworkPressure = val
				}
			}
		}

		r.store.Put(nodeID, sample)
	}

	w.WriteHeader(http.StatusOK)
}

func extractMetricValue(gaugeDPs []struct {
	AsDouble float64 `json:"asDouble"`
	AsInt    int64   `json:"asInt"`
}, sumDPs []struct {
	AsDouble float64 `json:"asDouble"`
	AsInt    int64   `json:"asInt"`
}) float64 {
	if len(gaugeDPs) > 0 {
		if gaugeDPs[0].AsDouble != 0 {
			return gaugeDPs[0].AsDouble
		}
		return float64(gaugeDPs[0].AsInt)
	}
	if len(sumDPs) > 0 {
		if sumDPs[0].AsDouble != 0 {
			return sumDPs[0].AsDouble
		}
		return float64(sumDPs[0].AsInt)
	}
	return 0
}
