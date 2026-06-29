package topology

import (
	"sync"
	"time"
)

type TopologyNode struct {
	NodeID   string
	Platform string
}

type TopologyEdge struct {
	From       string
	To         string
	Source     string // "trace" | "network" | "k8s" | "inferred"
	Confidence float64
	CallRate   float64 // calls per second if known
}

type TopologyGraph struct {
	Edges       []TopologyEdge
	Nodes       map[string]TopologyNode
	Source      string
	LastUpdated time.Time
}

// Manager aggregates topology knowledge from various deterministic sources.
type Manager struct {
	mu    sync.RWMutex
	graph TopologyGraph
}

// NewManager creates a new topology manager.
func NewManager() *Manager {
	return &Manager{
		graph: TopologyGraph{
			Edges:       make([]TopologyEdge, 0),
			Nodes:       make(map[string]TopologyNode),
			Source:      "empty",
			LastUpdated: time.Now(),
		},
	}
}

// AddTraceEdge adds an edge discovered via OpenTelemetry traces.
func (m *Manager) AddTraceEdge(from, to string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Ensure nodes exist
	if _, ok := m.graph.Nodes[from]; !ok {
		m.graph.Nodes[from] = TopologyNode{NodeID: from}
	}
	if _, ok := m.graph.Nodes[to]; !ok {
		m.graph.Nodes[to] = TopologyNode{NodeID: to}
	}

	// Update existing edge or add new one
	for i, edge := range m.graph.Edges {
		if edge.From == from && edge.To == to {
			// Increase confidence if seen multiple times via traces
			if edge.Source == "trace" {
				m.graph.Edges[i].CallRate += 1.0 // Simple counter for now
				m.graph.LastUpdated = time.Now()
				return
			}
			// Upgrade source to trace if it was inferred
			m.graph.Edges[i].Source = "trace"
			m.graph.Edges[i].Confidence = 0.95
			m.graph.LastUpdated = time.Now()
			return
		}
	}

	// New edge
	m.graph.Edges = append(m.graph.Edges, TopologyEdge{
		From:       from,
		To:         to,
		Source:     "trace",
		Confidence: 0.95, // Trace data is highly confident
		CallRate:   1.0,
	})
	m.graph.LastUpdated = time.Now()
}

// GetEdgePrior returns the Bayesian prior probability P(edge) given known topology.
func (m *Manager) GetEdgePrior(from, to string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, edge := range m.graph.Edges {
		if edge.From == from && edge.To == to {
			return edge.Confidence
		}
	}

	// If the nodes exist in our graph but we have no edge between them,
	// and we have a rich graph (e.g. from traces), the probability is low.
	// For now, if no edge is found, return the uniform prior (0.5) to let
	// the data speak for itself.
	return 0.5
}
