package metricsstore

import (
	"sort"
	"sync"
	"time"
)

// NodeSample is a single observation of a service node's queue metrics.
// Populated by the Prometheus poller or the /ingest REST endpoint.
type NodeSample struct {
	ArrivalRate     float64
	ServiceRate     float64
	QueueLength     float64
	ComputePressure float64
	MemoryPressure  float64
	NetworkPressure float64
	DominantSignal  string
	Timestamp       float64   // unix seconds (float for pipeline compatibility)
	WallTime        time.Time // wall-clock time for human logging
	MetricSource    string    // "otel" | "docker" | "synthetic"
	MetricQuality   float64   // 0.0-1.0 confidence in metrics
}

// Store is a concurrent-safe sliding-window time-series store.
// Each node retains the last WindowSize samples in insertion order.
// The store is the single source of truth for all real metric data:
// the Prometheus poller writes here, /ingest writes here, and the
// pipeline reads from here instead of generating synthetic data.
type Store struct {
	mu         sync.RWMutex
	nodes      map[string][]NodeSample
	windowSize int
}

// New creates a Store with the given sliding-window size.
// A minimum of 4 samples is enforced (pipeline minimum for correlation).
func New(windowSize int) *Store {
	if windowSize < 4 {
		windowSize = 4
	}
	return &Store{
		nodes:      make(map[string][]NodeSample),
		windowSize: windowSize,
	}
}

// Put records a sample for nodeID, evicting the oldest when the window is full.
func (s *Store) Put(nodeID string, sample NodeSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[nodeID] = append(s.nodes[nodeID], sample)
	if len(s.nodes[nodeID]) > s.windowSize {
		s.nodes[nodeID] = s.nodes[nodeID][len(s.nodes[nodeID])-s.windowSize:]
	}
}

// GetArrivalRateSeries returns the arrival-rate time series for nodeID
// as a plain float slice for Phase 1/3 signal processing.
// Returns nil when nodeID has no samples.
func (s *Store) GetArrivalRateSeries(nodeID string) []float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	samples := s.nodes[nodeID]
	if len(samples) == 0 {
		return nil
	}
	out := make([]float64, len(samples))
	for i, sm := range samples {
		out[i] = sm.ArrivalRate
	}
	return out
}

// GetServiceRateSeries returns the service-rate time series for nodeID.
func (s *Store) GetServiceRateSeries(nodeID string) []float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	samples := s.nodes[nodeID]
	if len(samples) == 0 {
		return nil
	}
	out := make([]float64, len(samples))
	for i, sm := range samples {
		out[i] = sm.ServiceRate
	}
	return out
}

// GetLatestSample returns the most recent sample for nodeID.
// The second return value is false when nodeID has no samples.
func (s *Store) GetLatestSample(nodeID string) (NodeSample, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	samples := s.nodes[nodeID]
	if len(samples) == 0 {
		return NodeSample{}, false
	}
	return samples[len(samples)-1], true
}

// GetSamples returns a copy of all retained samples for nodeID.
// The copy lets pipeline validation inspect real data without holding the
// store lock or allowing callers to mutate internal slices.
func (s *Store) GetSamples(nodeID string) []NodeSample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	samples := s.nodes[nodeID]
	if len(samples) == 0 {
		return nil
	}
	out := make([]NodeSample, len(samples))
	copy(out, samples)
	return out
}

// GetAllNodeIDs returns all known node IDs in deterministic sorted order.
func (s *Store) GetAllNodeIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.nodes))
	for id := range s.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// HasRealData reports whether the store contains at least one node with
// enough samples to run the full pipeline (minimum 4 data points).
func (s *Store) HasRealData() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, samples := range s.nodes {
		if len(samples) >= 4 {
			return true
		}
	}
	return false
}

// SampleCount returns the number of stored samples for nodeID.
func (s *Store) SampleCount(nodeID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.nodes[nodeID])
}

// NodeCount returns the number of distinct node IDs in the store.
func (s *Store) NodeCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.nodes)
}

// RemoveNode completely evicts a node's time series from the store.
// Used when a container is stopped/removed to prevent stale analysis.
func (s *Store) RemoveNode(nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nodes, nodeID)
}
