package phase5_insight

import (
	"sort"

	phase4 "absia/internal/intelligence/phase4_explanation"
	"absia/pkg/metricsstore"
)

// Intervention represents an SRE action (do-calculus).
type Intervention struct {
	Action      string
	TargetNode  string
	TargetValue float64
	Type        string // e.g. "Scale", "Restart"
}

// WhatIfResult holds the outcome of a do-calculus simulation.
type WhatIfResult struct {
	Action                   string    `json:"action"`
	RecoveryProbability      float64   `json:"recovery_probability"`
	EstimatedRecoveryMinutes int       `json:"estimated_recovery_minutes"`
	Risk                     string    `json:"risk"`
	ConfidenceInterval       []float64 `json:"confidence_interval"`
	ExpectedUtility          float64   `json:"-"` // Used internally for ranking
}

// SimulateWhatIf scenarios applies interventions to the Phase 4 SCM
// to estimate recovery probabilities using do-calculus and Monte Carlo.
func SimulateWhatIf(
	p4Graph *phase4.CausalGraph,
	dataset *phase4.Dataset,
	targetNode string,
	rootCause string,
	store *metricsstore.Store,
) []WhatIfResult {
	if p4Graph == nil || dataset == nil {
		return nil
	}

	// 1. Generate possible interventions for the root cause
	interventions := []Intervention{
		{
			Action:      "Scale " + rootCause + " (+1 replica)",
			TargetNode:  rootCause,
			TargetValue: 0.5, // simulate 50% load reduction
			Type:        "Scale",
		},
		{
			Action:      "Restart " + rootCause,
			TargetNode:  rootCause,
			TargetValue: 0.0, // simulate queue clearing
			Type:        "Restart",
		},
	}

	var results []WhatIfResult

	// Get target's threshold from the store if possible, 
	// otherwise assume generic threshold (e.g., 0.8)
	targetThreshold := 0.8
	// In a real scenario, we'd query pkg/adaptive for the threshold.
	// For now, we assume that any load/value below 0.8 represents "recovery".

	// Number of Monte Carlo samples
	numSamples := 100

	for _, inv := range interventions {
		recoveredCount := 0
		var simulatedValues []float64

		// For each data point in the recent observational dataset,
		// simulate the counterfactual under the intervention.
		limit := len(dataset.Samples)
		if limit > numSamples {
			limit = numSamples
		}

		for i := 0; i < limit; i++ {
			sample := dataset.Samples[len(dataset.Samples)-1-i]

			// We will create a mutilated graph
			mutilatedGraph := cloneAndMutilate(p4Graph, inv.TargetNode, inv.TargetValue)
			
			// Abduct noise from the sample
			U := abductNoise(p4Graph, sample)
			
			// Apply noise to mutilated graph
			for id, n := range mutilatedGraph.Nodes {
				n.Noise = U[id]
			}
			
			// Evaluate
			outcome := evaluateGraph(mutilatedGraph)
			targetVal := outcome[targetNode]
			
			simulatedValues = append(simulatedValues, targetVal)

			if targetVal < targetThreshold {
				recoveredCount++
			}
		}

		prob := float64(recoveredCount) / float64(limit)
		
		// Sort simulated values to get 95% CI
		sort.Float64s(simulatedValues)
		lowerIdx := int(float64(limit) * 0.025)
		upperIdx := int(float64(limit) * 0.975)
		if upperIdx >= limit {
			upperIdx = limit - 1
		}
		
		var ci []float64
		if limit > 0 {
			ci = []float64{simulatedValues[lowerIdx], simulatedValues[upperIdx]}
		} else {
			ci = []float64{0, 0}
		}

		// Estimate recovery time (Scale is faster than Restart typically)
		recMin := 2
		risk := "low"
		if inv.Type == "Restart" {
			recMin = 5
			risk = "medium"
		}

		results = append(results, WhatIfResult{
			Action:                   inv.Action,
			RecoveryProbability:      prob,
			EstimatedRecoveryMinutes: recMin,
			Risk:                     risk,
			ConfidenceInterval:       ci,
		})
	}

	return results
}

// cloneAndMutilate creates a copy of the graph and applies the do-operator.
func cloneAndMutilate(graph *phase4.CausalGraph, targetNode string, targetValue float64) *phase4.CausalGraph {
	g := &phase4.CausalGraph{
		Nodes: make(map[string]*phase4.CausalNode),
		Edges: make([]*phase4.CausalEdge, 0),
	}

	for id, n := range graph.Nodes {
		newNode := &phase4.CausalNode{
			ID:    n.ID,
			Func:  n.Func,
			Noise: n.Noise,
		}
		
		// Apply do-operator: replace equation with constant
		if id == targetNode {
			newNode.Func = func(inputs []float64, noise float64) float64 {
				return targetValue
			}
		}
		g.Nodes[id] = newNode
	}

	for _, e := range graph.Edges {
		// Remove inbound edges to the intervened node
		if e.To.ID != targetNode {
			g.Edges = append(g.Edges, &phase4.CausalEdge{
				From:       g.Nodes[e.From.ID],
				To:         g.Nodes[e.To.ID],
				Lag:        e.Lag,
				Confidence: e.Confidence,
			})
		}
	}
	return g
}

func abductNoise(graph *phase4.CausalGraph, sample phase4.Sample) map[string]float64 {
	U := make(map[string]float64)
	for id, n := range graph.Nodes {
		val, ok := sample[id]
		if !ok {
			val = 0
		}
		
		// Calculate what the inputs were
		inputs := []float64{}
		for _, e := range graph.Edges {
			if e.To.ID == id {
				parentVal, ok := sample[e.From.ID]
				if ok {
					inputs = append(inputs, parentVal)
				} else {
					inputs = append(inputs, 0)
				}
			}
		}
		
		// For linear additive models: val = f(inputs) + noise  => noise = val - f(inputs)
		// We pass 0 as noise to evaluate the deterministic part.
		deterministicPart := n.Func(inputs, 0)
		U[id] = val - deterministicPart
	}
	return U
}

func evaluateGraph(graph *phase4.CausalGraph) map[string]float64 {
	// Simple topological sort and evaluation
	inDegree := make(map[string]int)
	for _, n := range graph.Nodes {
		inDegree[n.ID] = 0
	}
	for _, e := range graph.Edges {
		inDegree[e.To.ID]++
	}

	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	// Sort queue to be deterministic
	sort.Strings(queue)

	result := make(map[string]float64)
	for len(queue) > 0 {
		nodeID := queue[0]
		queue = queue[1:]

		n := graph.Nodes[nodeID]
		inputs := []float64{}
		
		// Collect parent values (order matters for the function, we should use edge order)
		// Assuming edges are deterministic, we gather inputs from parents that have already been evaluated.
		for _, e := range graph.Edges {
			if e.To.ID == nodeID {
				inputs = append(inputs, result[e.From.ID])
			}
		}
		
		val := n.Func(inputs, n.Noise)
		result[nodeID] = val

		for _, e := range graph.Edges {
			if e.From.ID == nodeID {
				inDegree[e.To.ID]--
				if inDegree[e.To.ID] == 0 {
					queue = append(queue, e.To.ID)
				}
			}
		}
	}
	return result
}
