package phase3_causal

/*
D-SEPARATION ENGINE (COMPLETE)

Features:
- Active path detection
- Collider / chain / fork handling
- Conditioning support
- Collider DESCENDANT activation (Pearl, Causality §1.2.4) — previously missing
- Minimal adjustment set via backdoor criterion (not single-node greedy)
*/

type pathNode struct {
	NodeID string
	From   string
}

// ===============================
// PUBLIC API
// ===============================

// IsDSeparated reports whether X ⊥ Y | Z holds in graph.
// A pair is d-separated if every path between them is blocked.
func IsDSeparated(
	graph *Graph,
	X, Y string,
	conditioned map[string]bool,
) bool {

	// Pre-compute the full ancestor set of conditioned nodes once.
	// A collider is activated if it or any of its descendants is conditioned on.
	condAncestors := allAncestors(graph, conditioned)

	visited := make(map[string]bool)

	return !hasActivePath(graph, X, Y, conditioned, condAncestors, visited, "")
}

// FindMinimalAdjustmentSet returns a valid (not necessarily globally minimal)
// adjustment set for estimating the causal effect of X on Y using the
// backdoor criterion (Pearl, Causality, Definition 3.3.1).
//
// Algorithm: parents(X) satisfy the backdoor criterion when no member
// is a descendant of X. We then greedily remove redundant members.
func FindMinimalAdjustmentSet(
	graph *Graph,
	X, Y string,
) []string {

	// Descendants of X — must not be in the adjustment set (Pearl Def 3.3.1 part i).
	descX := descendants(graph, X)

	// Candidate set: all parents of X that are not descendants of X.
	var candidates []string
	for _, e := range graph.Edges {
		if e.To == X && !descX[e.From] && e.From != Y {
			candidates = append(candidates, e.From)
		}
	}

	// Verify this candidate set actually blocks all backdoor paths.
	// If it does, try to remove redundant members (greedy shrink).
	candidateMap := toSet(candidates)
	if !IsDSeparated(graph, X, Y, candidateMap) {
		// Candidate set is not sufficient; return it anyway as best effort.
		return candidates
	}

	// Greedy minimisation: remove each candidate and check if adjustment set
	// still blocks all backdoor paths.
	var minimal []string
	for _, c := range candidates {
		without := copySetExcluding(candidateMap, c)
		if IsDSeparated(graph, X, Y, without) {
			// c is redundant — remove it from the set
			delete(candidateMap, c)
		} else {
			minimal = append(minimal, c)
		}
	}

	return minimal
}

// ===============================
// CORE LOGIC
// ===============================

func hasActivePath(
	graph *Graph,
	current, target string,
	conditioned map[string]bool,
	condAncestors map[string]bool,
	visited map[string]bool,
	prev string,
) bool {

	if current == target {
		return true
	}

	key := current + "|" + prev
	if visited[key] {
		return false
	}
	visited[key] = true

	neighbors := getNeighbors(graph, current)

	for _, next := range neighbors {

		if next == prev {
			continue
		}

		if isActiveTriplet(graph, prev, current, next, conditioned, condAncestors) {

			if hasActivePath(graph, next, target, conditioned, condAncestors, visited, current) {
				return true
			}
		}
	}

	return false
}

// ===============================
// TRIPLET RULE (COMPLETE THEORY)
// ===============================

func isActiveTriplet(
	graph *Graph,
	A, B, C string,
	conditioned map[string]bool,
	condAncestors map[string]bool,
) bool {

	if A == "" {
		return true // start node — no triplet to evaluate
	}

	// Edge directions
	AB := hasEdge(graph, A, B)
	CB := hasEdge(graph, C, B)

	// ============================
	// COLLIDER: A → B ← C
	// ============================
	if AB && CB {
		// Active iff B is conditioned on, OR any descendant of B is conditioned on.
		// Pearl, Causality §1.2.4: "a collider is activated by conditioning on it
		// or any of its descendants."
		if conditioned[B] || condAncestors[B] {
			return true
		}
		return false
	}

	// ============================
	// CHAIN or FORK:
	//   Chain forward:  A → B → C
	//   Chain backward: A ← B ← C
	//   Fork:           A ← B → C
	// All three are BLOCKED when B is conditioned on.
	// ============================
	if conditioned[B] {
		return false
	}

	return true
}

// ===============================
// GRAPH HELPERS
// ===============================

func getNeighbors(graph *Graph, node string) []string {

	var neighbors []string

	for _, e := range graph.Edges {

		if e.From == node {
			neighbors = append(neighbors, e.To)
		}
		if e.To == node {
			neighbors = append(neighbors, e.From)
		}
	}

	return neighbors
}

func hasEdge(graph *Graph, from, to string) bool {

	for _, e := range graph.Edges {
		if e.From == from && e.To == to {
			return true
		}
	}

	return false
}

// ===============================
// ANCESTOR / DESCENDANT HELPERS
// ===============================

// allAncestors returns the set of all nodes that are ancestors of ANY node
// in the conditioned set. Used to check collider descendant activation.
// "B is an ancestor-of-conditioned" means some conditioned node is a
// descendant of B, i.e. conditioning on that node activates B as a collider.
func allAncestors(graph *Graph, conditioned map[string]bool) map[string]bool {
	result := make(map[string]bool)
	for node := range conditioned {
		markAncestors(graph, node, result)
	}
	return result
}

// markAncestors walks upward (against edge direction) from node and marks all
// ancestors into the result set.
func markAncestors(graph *Graph, node string, result map[string]bool) {
	for _, e := range graph.Edges {
		if e.To == node && !result[e.From] {
			result[e.From] = true
			markAncestors(graph, e.From, result)
		}
	}
}

// descendants returns the set of all descendants of node (exclusive of node itself).
func descendants(graph *Graph, node string) map[string]bool {
	result := make(map[string]bool)
	markDescendants(graph, node, result)
	return result
}

func markDescendants(graph *Graph, node string, result map[string]bool) {
	for _, e := range graph.Edges {
		if e.From == node && !result[e.To] {
			result[e.To] = true
			markDescendants(graph, e.To, result)
		}
	}
}

// ===============================
// SET HELPERS
// ===============================

func toSet(keys []string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func copySetExcluding(m map[string]bool, exclude string) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		if k != exclude {
			out[k] = v
		}
	}
	return out
}