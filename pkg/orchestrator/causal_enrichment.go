package orchestrator

import (
	"sort"
	"strings"

	phase3 "absia/internal/intelligence/phase3_causal"
)

// ============================================================================
// D-SEPARATION ENRICHMENT
// ============================================================================

// DSepEnrichment holds the d-separation analysis applied to every candidate
// edge in the discovered causal graph.
type DSepEnrichment struct {
	// DSepConfirmed: edge key "src->tgt" for edges that are NOT d-separated
	// given the empty conditioning set — meaning an active causal path exists.
	DSepConfirmed map[string]bool

	// ConfoundedPairs: edge keys where d-separation holds unconditionally.
	// These are spurious correlations with no active causal path.
	ConfoundedPairs map[string]bool

	// AdjustmentSets: minimal sufficient adjustment set Z for each confirmed edge,
	// keyed by "src->tgt". Empty slice means no adjustment needed (direct, unconfounded).
	AdjustmentSets map[string][]string
}

// RunDSeparationEnrichment applies Pearl's d-separation criterion to every
// directed edge in the discovered graph.
//
// For each (from → to) edge:
//   - IsDSeparated(from, to, {}) == true  → mark as confounded (no active causal path).
//   - IsDSeparated(from, to, {}) == false → causal path confirmed; compute adjustment set.
//
// The FULL discovered graph is used (not per-hypothesis subgraphs) so that
// common-cause paths through unselected nodes are correctly detected.
func RunDSeparationEnrichment(graph *phase3.Graph) DSepEnrichment {
	result := DSepEnrichment{
		DSepConfirmed:   make(map[string]bool),
		ConfoundedPairs: make(map[string]bool),
		AdjustmentSets:  make(map[string][]string),
	}

	if graph == nil || len(graph.Edges) == 0 {
		return result
	}

	// Deduplicate edges and sort for deterministic processing.
	type edgePair struct{ from, to string }
	seen := make(map[string]bool, len(graph.Edges))
	pairs := make([]edgePair, 0, len(graph.Edges))
	for _, e := range graph.Edges {
		key := e.From + "->" + e.To
		if !seen[key] {
			seen[key] = true
			pairs = append(pairs, edgePair{e.From, e.To})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].from != pairs[j].from {
			return pairs[i].from < pairs[j].from
		}
		return pairs[i].to < pairs[j].to
	})

	for _, p := range pairs {
		key := p.from + "->" + p.to

		// Empty conditioning set: if d-separated here, correlation is spurious.
		if phase3.IsDSeparated(graph, p.from, p.to, map[string]bool{}) {
			result.ConfoundedPairs[key] = true
			continue
		}

		// Active causal path confirmed. Find minimal backdoor adjustment set.
		adjSet := phase3.FindMinimalAdjustmentSet(graph, p.from, p.to)
		result.DSepConfirmed[key] = true
		result.AdjustmentSets[key] = adjSet
	}

	return result
}

// ============================================================================
// BACKDOOR ENRICHMENT
// ============================================================================

// BackdoorEnrichment holds backdoor-adjusted causal effect estimates for each
// d-separation-confirmed edge in the graph.
type BackdoorEnrichment struct {
	// Effects: edge key "src->tgt" → estimated causal effect after backdoor adjustment.
	// This is the do-calculus estimate E[Y | do(X=x)] − E[Y | do(X=x')] per unit change.
	Effects map[string]float64

	// UsedAdjustments: edge key → adjustment nodes actually used in the estimation.
	// Empty means no confounders found; a direct effect estimate was used.
	UsedAdjustments map[string][]string
}

// RunBackdoorEnrichment computes backdoor-adjusted causal effect estimates for
// every d-separation-confirmed edge, skipping confounded pairs.
//
// Calls phase3.ComputeBackdoorEffect which:
//   1. Finds the backdoor adjustment set Z (parents of X not descendants of X).
//   2. Estimates E[Y|do(X)] = Σ_z E[Y|X,Z=z] × P(Z=z).
//   3. Falls back to direct effect when Z is empty.
func RunBackdoorEnrichment(
	graph *phase3.Graph,
	temporalGraph *phase3.TemporalGraph,
	dsep DSepEnrichment,
) BackdoorEnrichment {
	result := BackdoorEnrichment{
		Effects:         make(map[string]float64),
		UsedAdjustments: make(map[string][]string),
	}

	if graph == nil || temporalGraph == nil {
		return result
	}

	// Process only d-separation-confirmed edges in deterministic order.
	keys := make([]string, 0, len(dsep.DSepConfirmed))
	for k := range dsep.DSepConfirmed {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		// Split "from->to" key.
		idx := strings.Index(key, "->")
		if idx < 0 {
			continue
		}
		from := key[:idx]
		to := key[idx+2:]
		if from == "" || to == "" {
			continue
		}

		br := phase3.ComputeBackdoorEffect(graph, temporalGraph, from, to)
		result.Effects[key] = br.Effect
		result.UsedAdjustments[key] = br.UsedZ
	}

	return result
}
