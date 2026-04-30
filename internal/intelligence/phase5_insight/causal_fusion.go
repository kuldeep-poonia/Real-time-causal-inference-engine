package phase5_insight

/*
CAUSAL FUSION ENGINE (FINAL - SAFE)

- Phase 4 dominates
- Phase 3 optional
- Fully nil-safe
- No crashes
*/

type FusionResult struct {
	RootCauses []string
	Mediators  []string
	Rejected   []string
}

// ===============================
// ENTRY POINT
// ===============================

func FuseCausalResults(
	graph *CausalGraph,
	phase3Root string,
	phase4Causes []string,
	target string,
) FusionResult {

	// Guard: no fusion is possible without a graph.
	if graph == nil {
		return FusionResult{
			RootCauses: phase4Causes,
			Mediators:  nil,
			Rejected:   nil,
		}
	}

	result := FusionResult{}

	// PASS 1: Identify confounders and build rejected list
	rejected := make(map[string]bool)
	for _, node := range phase4Causes {
		if isConfounder(graph, node, target, phase4Causes) {
			result.Rejected = append(result.Rejected, node)
			rejected[node] = true
		}
	}

	// PASS 2: Classify remaining nodes as mediators or roots
	for _, node := range phase4Causes {
		if rejected[node] {
			continue // Already rejected
		}

		// mediator detection (considers rejected list)
		if isMediator(graph, node, target, phase4Causes, rejected) {
			result.Mediators = append(result.Mediators, node)
			continue
		}

		// root cause (default)
		result.RootCauses = append(result.RootCauses, node)
	}

	// reject phase3-only nodes
	if phase3Root != "" && !contains(phase4Causes, phase3Root) {
		result.Rejected = append(result.Rejected, phase3Root)
	}

	return result
}

// ===============================
// CONFOUNDER DETECTION
// ===============================

func isConfounder(graph *CausalGraph, node, target string, allCauses []string) bool {

	if graph == nil {
		return false
	}

	// FIXED: A confounder is a ROOT NODE that either:
	//
	// Case 1: Creates MULTIPLE INDEPENDENT PATHS to target
	//   Example: Z→A, Z→B, A→X, B→X, X→C creates 2 paths through A,B
	//
	// Case 2: Causes SPURIOUS nodes (nodes in allCauses that DON'T reach target)
	//   Example: Z→A, Z→B, A→C (B doesn't reach C)
	//   Z causes B but B is irrelevant to target, so Z should be rejected

	// Check if node is a root (no incoming edges)
	hasParent := false
	for _, e := range graph.Edges {
		if e.To.ID == node {
			hasParent = true
			break
		}
	}

	// Not a root = not a confounder
	if hasParent {
		return false
	}

	// Root node - find all direct children
	children := make([]string, 0)
	for _, e := range graph.Edges {
		if e.From.ID == node {
			children = append(children, e.To.ID)
		}
	}

	// If fewer than 2 children, cannot confound multiple paths
	if len(children) < 2 {
		return false
	}

	// Case 1: Check if multiple children reach target
	reachesTarget := 0
	for _, child := range children {
		if canReachTarget(graph, child, target, allCauses) {
			reachesTarget++
		}
	}

	// If 2+ children reach target, it's a confounder (multiple paths)
	if reachesTarget >= 2 {
		return true
	}

	// Case 2: Check if ANY child that is in allCauses does NOT reach target
	// This means the root causes spurious (target-irrelevant) nodes
	for _, child := range children {
		// Is this child in allCauses?
		isInCauses := false
		for _, cause := range allCauses {
			if cause == child {
				isInCauses = true
				break
			}
		}

		// If child is in allCauses but doesn't reach target, spurious
		if isInCauses && !canReachTarget(graph, child, target, allCauses) {
			return true // Confounder causes spurious nodes
		}
	}

	return false
}

// Helper: Check if a node can reach target through graph
func canReachTarget(graph *CausalGraph, from, target string, allCauses []string) bool {
	if from == target {
		return true
	}

	visited := make(map[string]bool)
	return dfsCanReach(graph, from, target, visited)
}

func dfsCanReach(graph *CausalGraph, curr, target string, visited map[string]bool) bool {
	if curr == target {
		return true
	}

	visited[curr] = true

	// Find outgoing edges from curr
	for _, e := range graph.Edges {
		if e.From.ID == curr && !visited[e.To.ID] {
			if dfsCanReach(graph, e.To.ID, target, visited) {
				return true
			}
		}
	}

	return false
}

// ===============================
// MEDIATOR DETECTION
// ===============================

func isMediator(graph *CausalGraph, node, target string, allCauses []string, rejected map[string]bool) bool {

	// Guard against nil graph.
	if graph == nil {
		return false
	}

	// FIXED: A mediator has:
	// 1. An incoming edge from a NON-REJECTED node (has non-rejected parent)
	// 2. AND either:
	//    a) Direct edge to target, OR
	//    b) Connects to another cause that eventually reaches target

	hasNonRejectedParent := false
	hasDirectEdge := false
	connectsToOtherCause := false

	for _, e := range graph.Edges {

		if e.To.ID == node && !rejected[e.From.ID] {
			hasNonRejectedParent = true
		}

		if e.From.ID == node && e.To.ID == target {
			hasDirectEdge = true
		}

		// Check if node connects to another cause (for transitive paths)
		if e.From.ID == node {
			for _, cause := range allCauses {
				if cause != node && !rejected[cause] && e.To.ID == cause {
					connectsToOtherCause = true
					break
				}
			}
		}
	}

	// Mediator if: has non-rejected parent AND (direct edge to target OR connects to other cause)
	return hasNonRejectedParent && (hasDirectEdge || connectsToOtherCause)
}

// ===============================
// HELPERS
// ===============================

func contains(arr []string, x string) bool {
	for _, v := range arr {
		if v == x {
			return true
		}
	}
	return false
}
