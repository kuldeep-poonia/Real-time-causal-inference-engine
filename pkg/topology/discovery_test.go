package topology

import (
	"math"
	"testing"
)

func TestTopologyManager(t *testing.T) {
	mgr := NewManager()

	if mgr.GetEdgePrior("A", "B") != 0.5 {
		t.Errorf("Expected uniform prior 0.5 for unknown edge")
	}

	mgr.AddTraceEdge("A", "B")
	
	if math.Abs(mgr.GetEdgePrior("A", "B")-0.95) > 1e-3 {
		t.Errorf("Expected high prior ~0.95 for known trace edge, got %f", mgr.GetEdgePrior("A", "B"))
	}
	
	// Add it again to increase confidence/callrate
	mgr.AddTraceEdge("A", "B")
	
	if mgr.graph.Edges[0].CallRate != 2.0 {
		t.Errorf("Expected call rate 2.0, got %f", mgr.graph.Edges[0].CallRate)
	}
}
