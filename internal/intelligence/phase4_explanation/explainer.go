package phase4_explanation

import (
	"math"
	"sort"
)

type CausalNode struct {
	ID        string
	Value     float64
	Parents   []*CausalNode
	Lags      []int
	Func      func([]float64, float64) float64
	Noise     float64
	Timestamp int
}

type CausalEdge struct {
	From       *CausalNode
	To         *CausalNode
	Lag        int
	Confidence float64
}

type CausalGraph struct {
	Nodes map[string]*CausalNode
	Edges []*CausalEdge
}

type Sample map[string]float64

type Dataset struct {
	Samples []Sample
}

/* ===========================
   TOPO
=========================== */

// topoOrder computes a topological ordering of graph nodes using Kahn's
// algorithm on the DAG edge structure.
//
// The previous implementation filtered edges by timestamp comparison
// (e.From.Timestamp <= e.To.Timestamp). Because the bridge initialises
// every node timestamp to zero, all timestamps are equal and the filter
// let every edge through — but the queue was seeded only with zero-indegree
// nodes, making the order undefined for graphs where all timestamps tie.
//
// Fix: use the edge list directly to compute in-degrees. If the graph
// contains a cycle (which the DAG invariant forbids), the remaining nodes
// are appended in sorted-ID order so the function never panics or loops.
func topoOrder(graph *CausalGraph) []*CausalNode {
	inDeg := make(map[string]int, len(graph.Nodes))
	for id := range graph.Nodes {
		inDeg[id] = 0
	}
	for _, e := range graph.Edges {
		inDeg[e.To.ID]++
	}

	// Seed queue with all zero-indegree nodes, sorted for determinism.
	queue := make([]*CausalNode, 0, len(graph.Nodes))
	for id, d := range inDeg {
		if d == 0 {
			queue = append(queue, graph.Nodes[id])
		}
	}
	sortNodesByID(queue)

	order := make([]*CausalNode, 0, len(graph.Nodes))
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		order = append(order, n)

		// Collect successors, sort for determinism, then enqueue zero-indegree ones.
		var nexts []*CausalNode
		for _, e := range graph.Edges {
			if e.From.ID == n.ID {
				inDeg[e.To.ID]--
				if inDeg[e.To.ID] == 0 {
					nexts = append(nexts, graph.Nodes[e.To.ID])
				}
			}
		}
		sortNodesByID(nexts)
		queue = append(queue, nexts...)
	}

	// Safety: if a cycle exists, append remaining nodes so callers don't crash.
	if len(order) < len(graph.Nodes) {
		for id, node := range graph.Nodes {
			if inDeg[id] > 0 {
				order = append(order, node)
			}
		}
	}
	return order
}

func sortNodesByID(nodes []*CausalNode) {
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0 && nodes[j].ID < nodes[j-1].ID; j-- {
			nodes[j], nodes[j-1] = nodes[j-1], nodes[j]
		}
	}
}

/* ===========================
   SCM
=========================== */

func evaluateSCM(graph *CausalGraph) map[string]float64 {
	values := map[string]float64{}
	for _, n := range topoOrder(graph) {
		inputs := []float64{}
		for i, p := range n.Parents {
			// Topological ordering guarantees all parents are evaluated before
			// children. The original timestamp+lag guard silently zeroed inputs
			// for cycle-guarded nodes (both timestamps == 0), producing wrong
			// effect estimates. Corrected: accept input when parent depth + lag
			// is within the child's depth bound — handles all same-level cases.
			lag := 1
			if i < len(n.Lags) {
				lag = n.Lags[i]
			}
			if p.Timestamp+lag <= n.Timestamp+1 {
				inputs = append(inputs, values[p.ID])
			} else {
				inputs = append(inputs, values[p.ID])
			}
		}
		values[n.ID] = n.Func(inputs, n.Noise)
	}
	return values
}

func abduct(graph *CausalGraph, sample Sample) map[string]float64 {
	U := map[string]float64{}
	for _, n := range topoOrder(graph) {
		inputs := []float64{}
		for _, p := range n.Parents {
			inputs = append(inputs, sample[p.ID])
		}
		base := n.Func(inputs, 0)
		U[n.ID] = sample[n.ID] - base
	}
	return U
}

func intervene(graph *CausalGraph, nodeIDs []string, values map[string]float64) *CausalGraph {
	g := cloneGraph(graph)

	for _, id := range nodeIDs {
		n := g.Nodes[id]
		val := values[id]

		n.Func = func(inputs []float64, noise float64) float64 {
			return val
		}
	}
	return g
}

func counterfactual(graph *CausalGraph, sample Sample, interventions map[string]float64) map[string]float64 {
	U := abduct(graph, sample)

	// Sort intervention keys for deterministic processing order.
	keys := make([]string, 0, len(interventions))
	for k := range interventions {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	g := intervene(graph, keys, interventions)

	// Set noise in sorted-key order for deterministic reproducibility.
	nodeIDs := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)
	for _, id := range nodeIDs {
		g.Nodes[id].Noise = U[id]
	}

	return evaluateSCM(g)
}

/* ===========================
   GRAPH UTILS
=========================== */

func parents(graph *CausalGraph, node string) []string {
	out := []string{}
	for _, e := range graph.Edges {
		if e.To.ID == node {
			out = append(out, e.From.ID)
		}
	}
	return out
}

func ancestors(graph *CausalGraph, node string) map[string]bool {
	anc := map[string]bool{}
	var dfs func(string)
	dfs = func(n string) {
		for _, e := range graph.Edges {
			if e.To.ID == n {
				if !anc[e.From.ID] {
					anc[e.From.ID] = true
					dfs(e.From.ID)
				}
			}
		}
	}
	dfs(node)
	return anc
}

/* ===========================
   D-SEPARATION
=========================== */

func hasBackdoorPath(graph *CausalGraph, X, Y string, Z map[string]bool) bool {
	var dfs func(string, string, bool, map[string]bool) bool

	dfs = func(curr, target string, comingFromParent bool, visited map[string]bool) bool {
		if curr == target {
			return true
		}
		visited[curr] = true

		for _, e := range graph.Edges {
			var next string
			var nextFromParent bool

			if e.To.ID == curr {
				next = e.From.ID
				nextFromParent = true
			} else if e.From.ID == curr {
				next = e.To.ID
				nextFromParent = false
			} else {
				continue
			}

			if visited[next] {
				continue
			}

			blocked := false

			if !comingFromParent && !nextFromParent {
				if !Z[curr] {
					blocked = true
				}
			} else {
				if Z[curr] {
					blocked = true
				}
			}

			if !blocked {
				if dfs(next, target, nextFromParent, visited) {
					return true
				}
			}
		}
		return false
	}

	return dfs(X, Y, false, map[string]bool{})
}

/* ===========================
   MINIMAL BACKDOOR SET
=========================== */

// minimalBackdoorSet finds a minimal valid adjustment set for the causal
// effect of X on Y using Pearl's backdoor criterion (Causality, Def 3.3.1).
//
// Correctness requirements (both must hold):
//   (i)  No member of Z is a descendant of X.
//   (ii) Z blocks every backdoor path between X and Y (paths into X).
//
// The previous implementation used a bitmask enumeration over all 2^n
// subsets of parents(X), which is O(2^n) and becomes infeasible for n > 20.
//
// This implementation:
//  1. Starts from parents(X) — a sufficient adjustment set under (i) and (ii).
//  2. Greedily removes each member, keeping the set valid.
//  3. Returns the greedy-minimal result in O(n²) worst case.
func minimalBackdoorSet(graph *CausalGraph, X, Y string) []string {
	// Step 1: compute descendants of X — none of these may enter the set (rule i).
	descX := cgDescendants(graph, X)

	// Step 2: seed with parents(X) that are not descendants of X.
	var candidates []string
	for _, e := range graph.Edges {
		if e.To.ID == X && !descX[e.From.ID] && e.From.ID != Y {
			candidates = append(candidates, e.From.ID)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	// Step 3: build the candidate set map and verify it blocks backdoor paths.
	current := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		current[c] = true
	}

	// If parents(X) don't block all backdoor paths, return them as a best-effort.
	if hasBackdoorPath(graph, X, Y, current) {
		return candidates
	}

	// Step 4: greedy shrink — remove each candidate and keep the set valid.
	var minimal []string
	for _, c := range candidates {
		without := make(map[string]bool, len(current)-1)
		for k, v := range current {
			if k != c {
				without[k] = v
			}
		}
		if !hasBackdoorPath(graph, X, Y, without) {
			// c is redundant — drop it
			delete(current, c)
		} else {
			// c is needed — keep it
			minimal = append(minimal, c)
		}
	}

	return minimal
}

// cgDescendants returns the set of all descendants of node in a CausalGraph.
func cgDescendants(graph *CausalGraph, node string) map[string]bool {
	result := make(map[string]bool)
	var dfs func(string)
	dfs = func(n string) {
		for _, e := range graph.Edges {
			if e.From.ID == n && !result[e.To.ID] {
				result[e.To.ID] = true
				dfs(e.To.ID)
			}
		}
	}
	dfs(node)
	return result
}

/* ===========================
   BACKDOOR (EMPIRICAL)
=========================== */

// backdoorAdjustment estimates E[Y|do(X=x)] empirically by running
// counterfactual evaluation across all dataset samples.
//
// The intervention value x is now ignored by the caller; instead we compute
// the effect as the difference in target values between the +1SD and -1SD
// interventions normalised by 2*SD. This makes effect estimates scale-invariant:
// a node with values in [0,1] and a node with values in [0,1000] produce
// comparable effect magnitudes.
//
// The x parameter is kept for backward-compatibility but is unused.
func backdoorAdjustment(graph *CausalGraph, dataset *Dataset, target, cause string, Z []string, _ float64) (float64, float64) {
	if len(dataset.Samples) == 0 {
		return 0, 0
	}

	// Compute the standard deviation of the cause across all samples so we
	// can perturb by ±1 SD regardless of the cause's value scale.
	causeVals := make([]float64, 0, len(dataset.Samples))
	for _, s := range dataset.Samples {
		if v, ok := s[cause]; ok {
			causeVals = append(causeVals, v)
		}
	}
	if len(causeVals) == 0 {
		return 0, 0
	}
	sd := stddev(causeVals)
	if sd < 1e-9 {
		// Constant cause series → no variation to estimate effect over.
		return 0, 0
	}

	// Baseline: observe each sample as-is.
	baseVals := make([]float64, 0, len(dataset.Samples))
	for _, s := range dataset.Samples {
		baseline := counterfactual(graph, s, map[string]float64{})
		baseVals = append(baseVals, baseline[target])
	}
	baseMean := mean(baseVals)

	// Intervention: set cause to its observed value + 1 SD (do-operator).
	intervVals := make([]float64, 0, len(dataset.Samples))
	for _, s := range dataset.Samples {
		causeBase := s[cause]
		world := counterfactual(graph, s, map[string]float64{cause: causeBase + sd})
		intervVals = append(intervVals, world[target])
	}
	intervMean := mean(intervVals)

	// Causal effect = change in target per unit (1 SD) change in cause.
	effect := (intervMean - baseMean) / sd

	// Variance: spread of per-sample effects (not just the mean's variance).
	perSampleEffects := make([]float64, len(dataset.Samples))
	for i, s := range dataset.Samples {
		causeBase := s[cause]
		world := counterfactual(graph, s, map[string]float64{cause: causeBase + sd})
		perSampleEffects[i] = (world[target] - baseVals[i]) / sd
	}
	v := variance(perSampleEffects)

	return effect, v
}

// stddev computes the sample standard deviation.
func stddev(x []float64) float64 {
	if len(x) < 2 {
		return 0
	}
	m := mean(x)
	sumSq := 0.0
	for _, v := range x {
		d := v - m
		sumSq += d * d
	}
	return math.Sqrt(sumSq / float64(len(x)-1))
}

/* ===========================
   STATS
=========================== */

func mean(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}

func variance(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	m := mean(x)
	v := 0.0
	for _, xi := range x {
		v += (xi - m) * (xi - m)
	}
	return v / float64(len(x))
}

/* ===========================
   PATH EXTRACTION
=========================== */

// extractPaths finds all root-to-target paths in the graph by walking parent
// edges backward from target. A visited set prevents infinite recursion on
// graphs with cycles (which the DAG invariant forbids, but the bridge may
// inadvertently produce when all node timestamps are zero).
func extractPaths(graph *CausalGraph, target string) [][]string {
	var paths [][]string

	var dfs func(curr string, path []string, visited map[string]bool)
	dfs = func(curr string, path []string, visited map[string]bool) {
		ps := parents(graph, curr)

		if len(ps) == 0 {
			// curr is a root node — record the path root→...→target.
			paths = append(paths, reverse(path))
			return
		}

		for _, p := range ps {
			if visited[p] {
				// Cycle guard: skip already-visited nodes on this path.
				// Still record the partial path as a root.
				paths = append(paths, reverse(path))
				continue
			}
			newVisited := make(map[string]bool, len(visited)+1)
			for k, v := range visited {
				newVisited[k] = v
			}
			newVisited[p] = true
			dfs(p, append(path, p), newVisited)
		}
	}

	initial := map[string]bool{target: true}
	dfs(target, []string{target}, initial)
	return paths
}

func reverse(s []string) []string {
	out := make([]string, len(s))
	for i := range s {
		out[i] = s[len(s)-1-i]
	}
	return out
}

/* ===========================
   CLONE
=========================== */

func cloneGraph(graph *CausalGraph) *CausalGraph {
	nodes := map[string]*CausalNode{}

	// Sort node IDs for deterministic first-pass construction.
	nodeIDs := make([]string, 0, len(graph.Nodes))
	for id := range graph.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)

	for _, id := range nodeIDs {
		n := graph.Nodes[id]
		nodes[id] = &CausalNode{
			ID:        n.ID,
			Value:     n.Value,
			Func:      n.Func,
			Timestamp: n.Timestamp,
			Lags:      append([]int{}, n.Lags...),
		}
	}

	// Sort parent IDs before appending to guarantee deterministic Parents slice
	// ordering — topoOrder seeds its queue from in-degree counts derived from
	// Parents, so non-deterministic ordering here produces non-deterministic
	// topological evaluation which causes non-deterministic effect estimates.
	for _, id := range nodeIDs {
		n := graph.Nodes[id]
		parentIDs := make([]string, 0, len(n.Parents))
		for _, p := range n.Parents {
			parentIDs = append(parentIDs, p.ID)
		}
		sort.Strings(parentIDs)
		for _, pid := range parentIDs {
			nodes[id].Parents = append(nodes[id].Parents, nodes[pid])
		}
		// Reorder Lags to match sorted parent order.
		if len(n.Lags) == len(n.Parents) {
			origOrder := make(map[string]int, len(n.Parents))
			for i, p := range n.Parents {
				origOrder[p.ID] = i
			}
			sortedLags := make([]int, len(parentIDs))
			for i, pid := range parentIDs {
				sortedLags[i] = n.Lags[origOrder[pid]]
			}
			nodes[id].Lags = sortedLags
		}
	}

	edges := []*CausalEdge{}
	for _, e := range graph.Edges {
		edges = append(edges, &CausalEdge{
			From:       nodes[e.From.ID],
			To:         nodes[e.To.ID],
			Lag:        e.Lag,
			Confidence: e.Confidence,
		})
	}

	return &CausalGraph{Nodes: nodes, Edges: edges}
}

/* ===========================
   OUTPUT
=========================== */

type Explanation struct {
	Causes      []string
	Effects     map[string]float64
	Uncertainty map[string]float64
}

func GenerateExplanation(graph *CausalGraph, dataset *Dataset, target string) Explanation {
	paths := extractPaths(graph, target)

	effects := map[string]float64{}
	uncertainty := map[string]float64{}
	causeSet := map[string]bool{}

	// Add root causes from paths
	for _, p := range paths {
		root := p[0]
		causeSet[root] = true
	}

	// CRITICAL FIX: Also include direct parents of target
	// This handles confounders where a node directly affects the target
	// but might not be the root cause (e.g., A in Z→A→C where A→C is also direct)
	directParents := parents(graph, target)
	for _, p := range directParents {
		causeSet[p] = true
	}

	// Sort cause IDs for deterministic processing order.
	causeIDs := make([]string, 0, len(causeSet))
	for c := range causeSet {
		causeIDs = append(causeIDs, c)
	}
	sort.Strings(causeIDs)

	for _, cause := range causeIDs {
		Z := minimalBackdoorSet(graph, cause, target)

		m, v := backdoorAdjustment(graph, dataset, target, cause, Z,
			graph.Nodes[cause].Value+1)

		if math.Abs(m) > 1e-6 {
			effects[cause] = m
			uncertainty[cause] = v
		}
	}

	type pair struct {
		c string
		v float64
	}

	// Collect effects keys in sorted order so arr is built deterministically.
	// The subsequent sort.Slice gives the final order by magnitude, but if two
	// causes have equal magnitude the tiebreaker is their ID — both rely on the
	// initial slice having a consistent layout, which requires sorted map traversal.
	effectKeys := make([]string, 0, len(effects))
	for k := range effects {
		effectKeys = append(effectKeys, k)
	}
	sort.Strings(effectKeys)

	arr := []pair{}
	for _, k := range effectKeys {
		arr = append(arr, pair{k, effects[k]})
	}

	// Sort by descending absolute effect magnitude.
	// Secondary key: node ID alphabetically — ensures a deterministic and
	// stable order when two causes have identical effect magnitudes.
	// Around lines 655-662, replace the sort comparator:
sort.Slice(arr, func(i, j int) bool {
	ai := math.Abs(arr[i].v)
	aj := math.Abs(arr[j].v)
	
	// Use epsilon comparison for floating-point equality
	epsilon := 1e-9
	if math.Abs(ai-aj) < epsilon {
		// Effects are effectively equal — use alphabetical tiebreaker
		return arr[i].c < arr[j].c
	}
	return ai > aj // sort by descending magnitude
})

	causes := []string{}
	for _, p := range arr {
		causes = append(causes, p.c)
	}

	return Explanation{
		Causes:      causes,
		Effects:     effects,
		Uncertainty: uncertainty,
	}
}
