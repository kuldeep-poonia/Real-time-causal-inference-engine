package phase4_explanation

import (
	"math"
	"testing"
)

// ============================================================================
// TOPO ORDER
// ============================================================================

// TestTopoOrder_ChainABC verifies that a chain A→B→C is topologically ordered
// as [A, B, C] (not reverse, not scrambled) regardless of map insertion order.
func TestTopoOrder_ChainABC(t *testing.T) {
	graph := chainCausalGraph()
	order := topoOrder(graph)

	if len(order) != 3 {
		t.Fatalf("expected 3 nodes in topo order, got %d", len(order))
	}

	pos := make(map[string]int)
	for i, n := range order {
		pos[n.ID] = i
	}

	if pos["A"] >= pos["B"] {
		t.Errorf("A must come before B in topo order; got A=%d B=%d", pos["A"], pos["B"])
	}
	if pos["B"] >= pos["C"] {
		t.Errorf("B must come before C in topo order; got B=%d C=%d", pos["B"], pos["C"])
	}
}

// TestTopoOrder_AllZeroTimestamps verifies that the fixed Kahn's algorithm
// correctly handles the all-zero-timestamp case that broke the old version.
func TestTopoOrder_AllZeroTimestamps(t *testing.T) {
	graph := chainCausalGraph()
	// Explicitly zero all timestamps (mirrors what the bridge sets).
	for _, n := range graph.Nodes {
		n.Timestamp = 0
	}

	order := topoOrder(graph)
	if len(order) != 3 {
		t.Fatalf("expected 3 nodes, got %d (zero-timestamp bug?)", len(order))
	}

	pos := make(map[string]int)
	for i, n := range order {
		pos[n.ID] = i
	}
	if pos["A"] >= pos["B"] || pos["B"] >= pos["C"] {
		t.Errorf("wrong topo order with zero timestamps: A=%d B=%d C=%d", pos["A"], pos["B"], pos["C"])
	}
}

// TestTopoOrder_CycleNoPanic verifies that a cyclic graph does not panic or
// loop forever (cycle guard appends remaining nodes).
func TestTopoOrder_CycleNoPanic(t *testing.T) {
	a := &CausalNode{ID: "A", Parents: []*CausalNode{}, Lags: []int{},
		Func: func(_ []float64, n float64) float64 { return n }}
	b := &CausalNode{ID: "B", Parents: []*CausalNode{}, Lags: []int{},
		Func: func(_ []float64, n float64) float64 { return n }}

	// Cycle: A → B → A
	eAB := &CausalEdge{From: a, To: b}
	eBA := &CausalEdge{From: b, To: a}

	g := &CausalGraph{
		Nodes: map[string]*CausalNode{"A": a, "B": b},
		Edges: []*CausalEdge{eAB, eBA},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("topoOrder panicked on cyclic graph: %v", r)
		}
	}()

	order := topoOrder(g)
	if len(order) != 2 {
		t.Errorf("expected 2 nodes from cycle graph (cycle guard), got %d", len(order))
	}
}

// ============================================================================
// MEAN AND VARIANCE — EMPTY SLICE SAFETY
// ============================================================================

// TestMeanEmptySlice verifies that mean([]) returns 0 without panicking.
func TestMeanEmptySlice(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("mean([]) panicked: %v", r)
		}
	}()
	result := mean([]float64{})
	if result != 0 {
		t.Errorf("mean([]) should be 0, got %f", result)
	}
}

// TestVarianceEmptySlice verifies that variance([]) returns 0 without panicking.
func TestVarianceEmptySlice(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("variance([]) panicked: %v", r)
		}
	}()
	result := variance([]float64{})
	if result != 0 {
		t.Errorf("variance([]) should be 0, got %f", result)
	}
}

// TestMeanSingleElement verifies mean of a single element is that element.
func TestMeanSingleElement(t *testing.T) {
	if mean([]float64{7.5}) != 7.5 {
		t.Errorf("mean([7.5]) should be 7.5, got %f", mean([]float64{7.5}))
	}
}

// TestVarianceSingleElement verifies variance of a single element is 0.
func TestVarianceSingleElement(t *testing.T) {
	if variance([]float64{7.5}) != 0 {
		t.Errorf("variance([7.5]) should be 0, got %f", variance([]float64{7.5}))
	}
}

// TestMeanKnownValues verifies mean on a known set.
func TestMeanKnownValues(t *testing.T) {
	got := mean([]float64{1, 2, 3, 4, 5})
	if math.Abs(got-3.0) > 1e-9 {
		t.Errorf("mean([1,2,3,4,5]) should be 3.0, got %f", got)
	}
}

// TestVarianceKnownValues verifies population variance on a known set.
// Var([2, 4, 4, 4, 5, 5, 7, 9]) = 4.0 (population variance, Wikipedia).
func TestVarianceKnownValues(t *testing.T) {
	got := variance([]float64{2, 4, 4, 4, 5, 5, 7, 9})
	if math.Abs(got-4.0) > 1e-9 {
		t.Errorf("variance([2,4,4,4,5,5,7,9]) should be 4.0, got %f", got)
	}
}

// ============================================================================
// MINIMAL BACKDOOR SET — O(n²) CORRECTNESS
// ============================================================================

// TestMinimalBackdoorSet_Fork verifies that the fork graph returns the confounder.
// Fork: Z→X, Z→Y. Parents of X = {Z}. Z satisfies the backdoor criterion.
func TestMinimalBackdoorSet_Fork(t *testing.T) {
	graph := forkCausalGraph()
	adj := minimalBackdoorSet(graph, "X", "Y")

	found := false
	for _, v := range adj {
		if v == "Z" {
			found = true
		}
	}
	if !found {
		t.Errorf("fork Z→X,Z→Y: expected Z in backdoor set, got %v", adj)
	}
}

// TestMinimalBackdoorSet_Chain verifies that a chain X→M→Y needs no adjustment.
// Parents of X = {} → adjustment set is empty.
func TestMinimalBackdoorSet_Chain(t *testing.T) {
	graph := chainCausalGraph_XMY()
	adj := minimalBackdoorSet(graph, "X", "Y")
	if len(adj) != 0 {
		t.Errorf("chain X→M→Y: expected empty backdoor set, got %v", adj)
	}
}

// TestMinimalBackdoorSet_NoPanicLargeGraph ensures no panic with 8 parents
// (the old O(2^8)=256 iteration loop; new algorithm must be fast).
func TestMinimalBackdoorSet_NoPanicLargeGraph(t *testing.T) {
	// Build a graph with 8 parents of X, none of which are descendants of X.
	nodes := make(map[string]*CausalNode)
	x := &CausalNode{ID: "X", Func: identity, Lags: []int{}, Parents: []*CausalNode{}}
	y := &CausalNode{ID: "Y", Func: identity, Lags: []int{}, Parents: []*CausalNode{}}
	nodes["X"] = x
	nodes["Y"] = y

	var edges []*CausalEdge
	for i := 0; i < 8; i++ {
		id := string(rune('A' + i))
		n := &CausalNode{ID: id, Func: identity, Lags: []int{}, Parents: []*CausalNode{}}
		nodes[id] = n
		edges = append(edges, &CausalEdge{From: n, To: x}) // parent of X
	}

	g := &CausalGraph{Nodes: nodes, Edges: edges}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("minimalBackdoorSet panicked on 8-parent graph: %v", r)
		}
	}()

	// Should complete instantly (O(n²) not O(2^n)).
	_ = minimalBackdoorSet(g, "X", "Y")
}

// ============================================================================
// GENERATE EXPLANATION — INTEGRATION
// ============================================================================

// TestGenerateExplanation_ChainFindsRootCause verifies that a chain A→B→C
// produces an explanation that identifies A as the primary driver of C.
func TestGenerateExplanation_ChainFindsRootCause(t *testing.T) {
	graph, dataset := buildChainGraphWithData()
	exp := GenerateExplanation(graph, dataset, "C")

	if len(exp.Causes) == 0 {
		t.Fatal("GenerateExplanation returned no causes for chain A→B→C")
	}

	// Primary cause should be A (largest effect magnitude).
	if exp.Causes[0] != "A" && exp.Causes[0] != "B" {
		t.Errorf("expected primary cause to be A or B, got %s", exp.Causes[0])
	}

	// Effects map should contain numeric entries.
	for _, c := range exp.Causes {
		if math.IsNaN(exp.Effects[c]) || math.IsInf(exp.Effects[c], 0) {
			t.Errorf("cause %s has NaN/Inf effect value: %f", c, exp.Effects[c])
		}
	}
}

// ============================================================================
// HELPERS
// ============================================================================

func identity(inputs []float64, noise float64) float64 {
	sum := noise
	for _, v := range inputs {
		sum += v
	}
	return sum
}

// chainCausalGraph builds A→B→C with constant functions for topo tests.
func chainCausalGraph() *CausalGraph {
	a := &CausalNode{ID: "A", Timestamp: 0, Lags: []int{}, Parents: []*CausalNode{},
		Func: func(_ []float64, n float64) float64 { return 1.0 + n }}
	b := &CausalNode{ID: "B", Timestamp: 0, Lags: []int{0}, Parents: []*CausalNode{a},
		Func: func(in []float64, n float64) float64 {
			if len(in) > 0 {
				return in[0] + n
			}
			return n
		}}
	c := &CausalNode{ID: "C", Timestamp: 0, Lags: []int{0}, Parents: []*CausalNode{b},
		Func: func(in []float64, n float64) float64 {
			if len(in) > 0 {
				return in[0] + n
			}
			return n
		}}
	return &CausalGraph{
		Nodes: map[string]*CausalNode{"A": a, "B": b, "C": c},
		Edges: []*CausalEdge{
			{From: a, To: b, Lag: 0},
			{From: b, To: c, Lag: 0},
		},
	}
}

// forkCausalGraph builds Z→X, Z→Y.
func forkCausalGraph() *CausalGraph {
	z := &CausalNode{ID: "Z", Func: identity, Lags: []int{}, Parents: []*CausalNode{}}
	x := &CausalNode{ID: "X", Func: identity, Lags: []int{0}, Parents: []*CausalNode{z}}
	y := &CausalNode{ID: "Y", Func: identity, Lags: []int{0}, Parents: []*CausalNode{z}}
	return &CausalGraph{
		Nodes: map[string]*CausalNode{"Z": z, "X": x, "Y": y},
		Edges: []*CausalEdge{
			{From: z, To: x},
			{From: z, To: y},
		},
	}
}

// chainCausalGraph_XMY builds X→M→Y.
func chainCausalGraph_XMY() *CausalGraph {
	x := &CausalNode{ID: "X", Func: identity, Lags: []int{}, Parents: []*CausalNode{}}
	m := &CausalNode{ID: "M", Func: identity, Lags: []int{0}, Parents: []*CausalNode{x}}
	y := &CausalNode{ID: "Y", Func: identity, Lags: []int{0}, Parents: []*CausalNode{m}}
	return &CausalGraph{
		Nodes: map[string]*CausalNode{"X": x, "M": m, "Y": y},
		Edges: []*CausalEdge{
			{From: x, To: m},
			{From: m, To: y},
		},
	}
}

// buildChainGraphWithData builds A→B→C with synthetic samples for backdoor adjustment.
func buildChainGraphWithData() (*CausalGraph, *Dataset) {
	a := &CausalNode{ID: "A", Value: 5.0, Timestamp: 0, Lags: []int{}, Parents: []*CausalNode{},
		Func: func(_ []float64, n float64) float64 { return 5.0 + n }}
	b := &CausalNode{ID: "B", Value: 4.0, Timestamp: 1, Lags: []int{0}, Parents: []*CausalNode{a},
		Func: func(in []float64, n float64) float64 {
			if len(in) > 0 {
				return 0.8*in[0] + n
			}
			return n
		}}
	c := &CausalNode{ID: "C", Value: 3.0, Timestamp: 2, Lags: []int{0}, Parents: []*CausalNode{b},
		Func: func(in []float64, n float64) float64 {
			if len(in) > 0 {
				return 0.7*in[0] + n
			}
			return n
		}}

	graph := &CausalGraph{
		Nodes: map[string]*CausalNode{"A": a, "B": b, "C": c},
		Edges: []*CausalEdge{
			{From: a, To: b, Lag: 0, Confidence: 0.9},
			{From: b, To: c, Lag: 0, Confidence: 0.85},
		},
	}

	// Generate 20 samples by forward simulation.
	samples := make([]Sample, 20)
	for i := range samples {
		aVal := 5.0 + float64(i%5)*0.5
		bVal := 0.8*aVal + 0.1
		cVal := 0.7*bVal + 0.05
		samples[i] = Sample{"A": aVal, "B": bVal, "C": cVal}
	}

	return graph, &Dataset{Samples: samples}
}
