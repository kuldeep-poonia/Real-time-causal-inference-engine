package metricsstore

import (
	"testing"
)

func TestStore_RemoveNode(t *testing.T) {
	store := New(10)
	store.Put("node1", NodeSample{ArrivalRate: 1.0})
	store.Put("node2", NodeSample{ArrivalRate: 2.0})

	if store.NodeCount() != 2 {
		t.Fatalf("expected 2 nodes, got %d", store.NodeCount())
	}

	store.RemoveNode("node1")

	if store.NodeCount() != 1 {
		t.Fatalf("expected 1 node after removal, got %d", store.NodeCount())
	}

	nodes := store.GetAllNodeIDs()
	if len(nodes) != 1 || nodes[0] != "node2" {
		t.Fatalf("expected only node2, got %v", nodes)
	}

	samples := store.GetSamples("node1")
	if len(samples) != 0 {
		t.Fatalf("expected 0 samples for removed node, got %d", len(samples))
	}
}
