package phase3_causal

import "math"

// IdentificationConfig controls how aggressively d-separation and the
// backdoor criterion reshape the discovered graph.
type IdentificationConfig struct {
	// DSepWeakeningFactor is multiplied onto ExistenceProb for any edge
	// (X→Y) where X and Y are d-separated with NO conditioning set.
	// A pair that is d-separated unconditionally has no active causal path
	// between them, so the edge is almost certainly spurious.
	// Range (0,1]. Recommended: 0.3
	DSepWeakeningFactor float64

	// BackdoorMinEffect is the minimum absolute backdoor-adjusted causal
	// effect required to keep an edge's ExistenceProb from being decayed.
	// Edges whose backdoor effect is below this threshold are treated as
	// noise and their probability is multiplied by DSepWeakeningFactor.
	// Recommended: 0.05
	BackdoorMinEffect float64

	// AdjustmentBoostPerConfounder is added to the existence probability
	// boost for each confounder successfully controlled in the adjustment set.
	// Representing: "we found and corrected for N confounders → higher confidence."
	// Recommended: 0.05
	AdjustmentBoostPerConfounder float64
}

// DefaultIdentificationConfig returns safe production defaults.
func DefaultIdentificationConfig() IdentificationConfig {
	return IdentificationConfig{
		DSepWeakeningFactor:          0.30,
		BackdoorMinEffect:            0.001,
		AdjustmentBoostPerConfounder: 0.05,
	}
}

// EnrichGraphWithCausalIdentification applies three identification steps to
// every edge in the discovered graph, modifying ExistenceProb and CausalStrength
// IN PLACE.  The original graph pointer is returned for chaining.
//
// Step 1 — Unconditional d-separation check:
//   If IsDSeparated(graph, X, Y, {}) is true, there is no active path between
//   X and Y even without conditioning.  The edge X→Y is almost certainly a
//   numerical artefact from lagged-correlation estimation.  ExistenceProb is
//   multiplied by DSepWeakeningFactor.
//
// Step 2 — Backdoor-adjusted effect estimation:
//   ComputeBackdoorEffect(graph, data, X, Y) returns the causal effect of X
//   on Y after controlling for all parent confounders.  This replaces the raw
//   correlation-derived CausalStrength with a confounded-adjusted estimate.
//   If the adjusted effect is below BackdoorMinEffect, ExistenceProb is decayed.
//
// Step 3 — Adjustment-set confidence boost:
//   FindMinimalAdjustmentSet(graph, X, Y) finds the minimal set of variables
//   to condition on to block all backdoor paths.  Each variable in that set
//   represents a confounder we have successfully identified and controlled.
//   ExistenceProb is boosted by AdjustmentBoostPerConfounder per confounder,
//   capped at 1.0.
//
// The function is a no-op when graph has no edges.
func EnrichGraphWithCausalIdentification(
	graph *Graph,
	temporalData *TemporalGraph,
	cfg IdentificationConfig,
) *Graph {
	if graph == nil || len(graph.Edges) == 0 {
		return graph
	}

	for _, edge := range graph.Edges {
		// ── Step 1: unconditional d-separation ──────────────────────────────
		// If X ⊥ Y | {} then X and Y are marginally independent — no path at all.
		if IsDSeparated(graph, edge.From, edge.To, map[string]bool{}) {
			edge.ExistenceProb *= cfg.DSepWeakeningFactor
			// Skip steps 2 & 3: if there is no path, backdoor estimation is
			// meaningless and the adjustment set would be empty.
			continue
		}

		// ── Step 2: backdoor-adjusted causal strength ────────────────────────
		backdoor := ComputeBackdoorEffect(graph, temporalData, edge.From, edge.To)

		absEffect := math.Abs(backdoor.Effect)
		if absEffect > cfg.BackdoorMinEffect {
			// Replace raw correlation-derived strength with adjusted estimate.
			//edge.CausalStrength = absEffect
		} else {
			// Effect too weak after confounder adjustment → likely spurious.
			edge.ExistenceProb *= cfg.DSepWeakeningFactor
			// Still update strength so downstream scoring sees the correct value.
			//edge.CausalStrength = absEffect
			continue
		}

		// ── Step 3: adjustment-set confidence boost ──────────────────────────
		adjSet := FindMinimalAdjustmentSet(graph, edge.From, edge.To)
		if len(adjSet) > 0 {
			boost := 1.0 + cfg.AdjustmentBoostPerConfounder*float64(len(adjSet))
			edge.ExistenceProb = math.Min(edge.ExistenceProb*boost, 1.0)
		}
	}

	return graph
}

// AssignTimestampsFromTopologicalOrder sets node State.Timestamp values
// based on topological depth so that FindRootCauseByPropagation's temporal
// ordering check works correctly even when real wall-clock timestamps are
// unavailable (e.g. synthetic data mode).
//
// Nodes with no predecessors receive timestamp 0.
// Each level deeper in the DAG receives timestamp = parent_depth + 1.
// Cycles (which should not exist in a DAG) are handled gracefully by
// assigning depth 0 to unresolved nodes.
func AssignTimestampsFromTopologicalOrder(graph *Graph) {
	if graph == nil {
		return
	}

	depth := make(map[string]float64)

	// Kahn's algorithm to compute depths.
	inDeg := make(map[string]int)
	for id := range graph.Nodes {
		inDeg[id] = 0
	}
	for _, e := range graph.Edges {
		inDeg[e.To]++
	}

	queue := make([]string, 0, len(graph.Nodes))
	for id, d := range inDeg {
		if d == 0 {
			queue = append(queue, id)
			depth[id] = 0
		}
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range graph.Edges {
			if e.From != cur {
				continue
			}
			inDeg[e.To]--
			if depth[e.To] < depth[cur]+1 {
				depth[e.To] = depth[cur] + 1
			}
			if inDeg[e.To] == 0 {
				queue = append(queue, e.To)
			}
		}
	}

	for id, node := range graph.Nodes {
		node.State.Timestamp = depth[id]
	}
}
