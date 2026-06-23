package orchestrator

import (
	"math"
	"testing"

	"absia/pkg/data"
	"absia/pkg/metricsstore"
)

// buildTestStore creates a metricsstore.Store pre-populated with deterministic
// samples for the given nodes. Uses GenerateRealisticCausalDataWithSeed which
// is the only remaining synthetic generator — kept in tests, removed from prod.
func buildTestStore(baseRate float64, seed int64) *metricsstore.Store {
	ds := generateTestDataset(baseRate, 10, seed)
	store := metricsstore.New(60)
	for _, point := range ds.Points {
		for _, nodeID := range ds.Nodes {
			store.Put(nodeID, metricsstore.NodeSample{
				ArrivalRate: point.Values[nodeID],
				ServiceRate: 1.0,
				QueueLength: point.Values[nodeID] * 10,
				Timestamp:   point.Timestamp,
			})
		}
	}
	return store
}

// generateTestDataset creates a deterministic A→B→C causal chain for tests.
// Inlined here because the generator was removed from pkg/data (production code).
func generateTestDataset(baseRate float64, timeSteps int, seed int64) *data.Dataset {
	import_rng := newTestRng(seed)
	ds := &data.Dataset{
		Points: make([]data.DataPoint, 0, timeSteps),
		Nodes:  []string{"A", "B", "C"},
	}
	aVals := make([]float64, timeSteps)
	bVals := make([]float64, timeSteps)
	cVals := make([]float64, timeSteps)
	aVals[0] = baseRate + (import_rng()-0.5)*2*0.5
	bVals[0] = 0.5 * aVals[0]
	cVals[0] = 0.5 * bVals[0]
	for t := 1; t < timeSteps; t++ {
		aVals[t] = baseRate + (import_rng()-0.5)*2*0.5
		bVals[t] = 0.7*aVals[t-1] + 0.3*bVals[t-1] + (import_rng()-0.5)*2*0.5
		if bVals[t] < 0 { bVals[t] = 0 }
		cVals[t] = 0.8*bVals[t-1] + 0.2*cVals[t-1] + (import_rng()-0.5)*2*0.5
		if cVals[t] < 0 { cVals[t] = 0 }
	}
	for t := 0; t < timeSteps; t++ {
		ds.Points = append(ds.Points, data.DataPoint{
			Timestamp: float64(t),
			Values:    map[string]float64{"A": aVals[t], "B": bVals[t], "C": cVals[t]},
			Missing:   make(map[string]bool),
		})
	}
	return ds
}

// newTestRng returns a simple closure-based LCG for test data generation.
// Avoids importing math/rand in tests to keep the determinism self-contained.
func newTestRng(seed int64) func() float64 {
	s := uint64(seed)
	return func() float64 {
		s = s*6364136223846793005 + 1442695040888963407
		return float64(s>>11) / float64(1<<53)
	}
}

// ── Input validation ──────────────────────────────────────────────────────────

func TestPipelineRejectsZeroServiceRate(t *testing.T) {
	store := buildTestStore(10.0, 42)
	store.Put("A", metricsstore.NodeSample{ArrivalRate: 10.0, ServiceRate: 0.0, QueueLength: 1.0, Timestamp: 100})
	_, err := ExecuteFullPipelineFromStore("A", store)
	if err == nil {
		t.Error("expected error for serviceRate=0, got nil")
	}
}

func TestPipelineRejectsNegativeServiceRate(t *testing.T) {
	store := buildTestStore(5.0, 42)
	store.Put("A", metricsstore.NodeSample{ArrivalRate: 10.0, ServiceRate: -1.0, QueueLength: 1.0, Timestamp: 100})
	_, err := ExecuteFullPipelineFromStore("A", store)
	if err == nil {
		t.Error("expected error for serviceRate=-1, got nil")
	}
}

func TestPipelineRejectsNegativeArrivalRate(t *testing.T) {
	store := buildTestStore(5.0, 42)
	store.Put("A", metricsstore.NodeSample{ArrivalRate: -1.0, ServiceRate: 10.0, QueueLength: 1.0, Timestamp: 100})
	_, err := ExecuteFullPipelineFromStore("A", store)
	if err == nil {
		t.Error("expected error for arrivalRate=-1, got nil")
	}
}

func TestPipelineRejectsNilStore(t *testing.T) {
	_, err := ExecuteFullPipelineFromStore("A", nil)
	if err == nil {
		t.Error("expected error for nil store, got nil")
	}
}

func TestPipelineRejectsEmptyStore(t *testing.T) {
	store := metricsstore.New(60)
	_, err := ExecuteFullPipelineFromStore("A", store)
	if err == nil {
		t.Error("expected error for empty store, got nil")
	}
}

// ── Determinism 

func TestPipelineDeterminism(t *testing.T) {
	store := buildTestStore(10.0, 42)
	r1, err1 := ExecuteFullPipelineFromStore("A", store)
	r2, err2 := ExecuteFullPipelineFromStore("A", store)
	if err1 != nil || err2 != nil {
		t.Fatalf("pipeline error: %v / %v", err1, err2)
	}
	if r1.Phase1NodeState.Load != r2.Phase1NodeState.Load {
		t.Errorf("Phase1 load not deterministic: %.6f vs %.6f", r1.Phase1NodeState.Load, r2.Phase1NodeState.Load)
	}
	if len(r1.Phase2Patterns) != len(r2.Phase2Patterns) {
		t.Errorf("Phase2 pattern count not deterministic: %d vs %d", len(r1.Phase2Patterns), len(r2.Phase2Patterns))
	}
	if r1.Phase3Result != nil && r2.Phase3Result != nil {
		if r1.Phase3Result.Target != r2.Phase3Result.Target {
			t.Errorf("Phase3 target not deterministic: %s vs %s", r1.Phase3Result.Target, r2.Phase3Result.Target)
		}
		if math.Abs(r1.Phase3Result.Score-r2.Phase3Result.Score) > 1e-12 {
			t.Errorf("Phase3 score not deterministic: %.12f vs %.12f", r1.Phase3Result.Score, r2.Phase3Result.Score)
		}
	}
	if len(r1.Phase5Actions) != len(r2.Phase5Actions) {
		t.Errorf("Phase5 action count not deterministic: %d vs %d", len(r1.Phase5Actions), len(r2.Phase5Actions))
	}
}

// ── Data source 

func TestDataSourceIsAlwaysReal(t *testing.T) {
	store := buildTestStore(10.0, 42)
	result, err := ExecuteFullPipelineFromStore("A", store)
	if err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	if result.DataSource != "real" {
		t.Errorf("expected DataSource=real, got %q", result.DataSource)
	}
}

// ── Utilities ──

func TestSortedKeys(t *testing.T) {
	m := map[string]float64{"C": 1, "A": 2, "B": 3}
	k := sortedKeys(m)
	if len(k) != 3 || k[0] != "A" || k[1] != "B" || k[2] != "C" {
		t.Errorf("sortedKeys wrong order: %v", k)
	}
}

func TestBuildSignalMatrix(t *testing.T) {
	ds := generateTestDataset(5.0, 20, 1)
	m := buildSignalMatrix(ds)
	if len(m) != 20 {
		t.Errorf("expected 20 rows, got %d", len(m))
	}
	for i, row := range m {
		if len(row) != len(ds.Nodes) {
			t.Errorf("row %d: expected %d cols, got %d", i, len(ds.Nodes), len(row))
		}
		for j, v := range row {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Errorf("NaN/Inf in signal matrix[%d][%d]", i, j)
			}
		}
	}
}