package phase3_causal

import (
	"math"
	"sort"
	"strings"
)

/*
CAUSAL BUILDER (FINAL++)

ye version:
- deterministic ranking
- effect-aware greedy selection
- recursive chain expansion
- conflict resolution (direct vs indirect)
*/

/*
CONFIG
*/
type CausalBuilderConfig struct {
	MinProbability float64
	MinStrength    float64

	MaxCauses int
}

/*
MAIN
*/
func BuildCausalHypotheses(
	graph *Graph,
	cfg CausalBuilderConfig,
) []CausalHypothesis {

	var hypotheses []CausalHypothesis

	strongEdges := filterStrongEdges(graph.Edges, cfg)
	targetMap := groupByTarget(strongEdges)

	// Sort target keys so hypothesis ordering is deterministic across runs.
	// Go map iteration is randomized; without this sort, RunCausalInference's
	// TopK selection is non-deterministic when multiple targets score equally.
	targets := make([]string, 0, len(targetMap))
	for t := range targetMap {
		targets = append(targets, t)
	}
	sort.Strings(targets)

	for _, target := range targets {
		edges := targetMap[target]

		sortEdges(edges)

		selected := greedySelectEffectAware(edges, cfg.MaxCauses)

		expanded := expandCausalChainRecursive(selected, graph)

		resolved := resolveConflicts(expanded, target)

		subgraph := buildSubgraph(graph, resolved, target)

		prob := computeCombinedProbability(resolved)
		variance := computeWeightedVariance(resolved)

		h := CausalHypothesis{
			ID:     generateStableID(target, resolved),
			Target: target,

			Subgraph: subgraph,

			Probability: prob,
			Mean:        prob,
			Variance:    variance,

			Description: "",
		}

		hypotheses = append(hypotheses, h)
	}

	return hypotheses
}

/*
FILTER
*/
func filterStrongEdges(edges []*Edge, cfg CausalBuilderConfig) []*Edge {
	var result []*Edge

	for _, e := range edges {
		if e.ExistenceProb >= cfg.MinProbability &&
	abs(e.CausalStrength) >= cfg.MinStrength &&
	len(e.SourceSeries) > 3 &&
	len(e.TargetSeries) > 3 {
			result = append(result, e)
		}
	}

	return result
}

/*
GROUP
*/
func groupByTarget(edges []*Edge) map[string][]*Edge {
	result := make(map[string][]*Edge)

	for _, e := range edges {
		result[e.To] = append(result[e.To], e)
	}

	return result
}

/*
SORT
*/
func sortEdges(edges []*Edge) {
	sort.Slice(edges, func(i, j int) bool {

		ti := computeTemporalCausality(edges[i].SourceSeries, edges[i].TargetSeries)
		tj := computeTemporalCausality(edges[j].SourceSeries, edges[j].TargetSeries)

		si := edges[i].ExistenceProb * math.Abs(edges[i].CausalStrength) * ti
		sj := edges[j].ExistenceProb * math.Abs(edges[j].CausalStrength) * tj

		return si > sj
	})
}

/*
GREEDY (EFFECT-AWARE)

avoid blocking chains prematurely
*/
func greedySelectEffectAware(edges []*Edge, max int) []*Edge {

	var selected []*Edge

	for _, e := range edges {
		if len(selected) >= max {
			break
		}

		// allow multiple edges from same source if useful
		selected = append(selected, e)
	}

	return selected
}

/*
RECURSIVE CHAIN EXPANSION
*/
func expandCausalChainRecursive(
	initial []*Edge,
	graph *Graph,
) []*Edge {

	visited := make(map[string]bool)
	var result []*Edge

	var dfs func(edge *Edge)
	dfs = func(edge *Edge) {

		key := edge.From + "->" + edge.To
		if visited[key] {
			return
		}

		visited[key] = true
		result = append(result, edge)

		// find parents recursively
		for _, e := range graph.Edges {
			if e.To == edge.From {
				dfs(e)
			}
		}
	}

	for _, e := range initial {
		dfs(e)
	}

	return result
}

/*
CONFLICT RESOLUTION

prefer:
- shorter path
- higher probability
*/
func resolveConflicts(edges []*Edge, target string) []*Edge {

	// group paths by source → target
	pathMap := make(map[string][]*Edge)

	for _, e := range edges {
		pathMap[e.From] = append(pathMap[e.From], e)
	}

	// Sort sources for deterministic output order.
	sources := make([]string, 0, len(pathMap))
	for src := range pathMap {
		sources = append(sources, src)
	}
	sort.Strings(sources)

	var result []*Edge

	for _, src := range sources {
		group := pathMap[src]

		// pick best edge for each source
		best := group[0]
		bestScore := best.ExistenceProb * math.Abs(best.CausalStrength)

		for _, e := range group {
			score := e.ExistenceProb * math.Abs(e.CausalStrength)

			if score > bestScore {
				best = e
				bestScore = score
			}
		}

		result = append(result, best)
	}

	return result
}

/*
SUBGRAPH
*/
func buildSubgraph(
	full *Graph,
	edges []*Edge,
	target string,
) *Graph {

	sub := &Graph{
		Nodes:   make(map[string]*Node),
		Edges:   []*Edge{},
		Factors: []*CausalFactor{},
	}

	sub.Nodes[target] = full.Nodes[target]

	for _, e := range edges {

		// ✅ attach series
		if nodeFrom, ok := full.Nodes[e.From]; ok {
			e.SourceSeries = nodeFrom.Series
			sub.Nodes[e.From] = nodeFrom
		}

		if nodeTo, ok := full.Nodes[e.To]; ok {
			e.TargetSeries = nodeTo.Series
		}

		sub.Edges = append(sub.Edges, e)
	}

	// factors
	for _, f := range full.Factors {
		if f.Target == target {
			sub.Factors = append(sub.Factors, f)
		}
	}

	return sub
}
/*
COMBINED PROB
*/
func computeCombinedProbability(edges []*Edge) float64 {

	prod := 1.0

	for _, e := range edges {

	temporal := computeTemporalCausality(e.SourceSeries, e.TargetSeries)

	prod *= (1.0 - e.ExistenceProb*temporal)
}

	return 1.0 - prod
}

/*
WEIGHTED VARIANCE
*/
func computeWeightedVariance(edges []*Edge) float64 {

	var num float64
	var den float64

	for _, e := range edges {
		w := e.ExistenceProb

		num += e.Variance * w
		den += w
	}

	if den == 0 {
		return 1
	}

	return num / den
}

/*
STABLE ID
*/
func generateStableID(target string, edges []*Edge) string {

	var sources []string

	for _, e := range edges {
		sources = append(sources, e.From)
	}

	sort.Strings(sources)

	return target + "_" + strings.Join(sources, "_")
}

/*
HELPER
*/
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}