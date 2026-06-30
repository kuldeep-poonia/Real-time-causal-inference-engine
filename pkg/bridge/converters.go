package bridge

import (
	"fmt"
	"sort"

	phase3 "absia/internal/intelligence/phase3_causal"
	phase4 "absia/internal/intelligence/phase4_explanation"
	phase5 "absia/internal/intelligence/phase5_insight"
)

/*
BRIDGE PACKAGE — Type Converters

Adapts outputs from one phase to inputs for the next phase.
NO modifications to Phase 3, 4, 5 logic — only format conversion.
*/

// ============================================================================
// PHASE 3 → PHASE 4 CONVERTER
// ============================================================================

/*
ConvertPhase3ResultToPhase4Graph converts Phase 3's causal inference results
into Phase 4's CausalGraph format for explanation generation.

Input: Phase 3 InferenceResult (causal links, root cause)
Output: Phase 4 CausalGraph (structural causal model)
*/
func ConvertPhase3ResultToPhase4Graph(
	result phase3.InferenceResult,
	graph *phase3.Graph,
	dataset *phase4.Dataset,
) *phase4.CausalGraph {

	if graph == nil {
		return nil
	}

	// Create Phase 4 graph structure
	cg := &phase4.CausalGraph{
		Nodes: make(map[string]*phase4.CausalNode),
		Edges: make([]*phase4.CausalEdge, 0),
	}

	// Convert nodes
	for nodeID, node := range graph.Nodes {
		cg.Nodes[nodeID] = &phase4.CausalNode{
			ID:        nodeID,
			Value:     node.State.Load, // Use ρ = λ/μ as node value
			Parents:   make([]*phase4.CausalNode, 0),
			Lags:      make([]int, 0),
			Timestamp: int(node.State.Timestamp),
			Noise:     0.0,
			// Func will be set after parents are assigned and sorted
		}
	}

	// Convert edges
	for _, edge := range graph.Edges {
		if fromNode, exists := cg.Nodes[edge.From]; exists {
			if toNode, exists := cg.Nodes[edge.To]; exists {
				// Create edge
				cEdge := &phase4.CausalEdge{
					From:       fromNode,
					To:         toNode,
					Lag:        1, // Default lag from Phase 3 temporal structure
					Confidence: edge.ExistenceProb,
				}
				cg.Edges = append(cg.Edges, cEdge)

				// Update parent references
				toNode.Parents = append(toNode.Parents, fromNode)
				toNode.Lags = append(toNode.Lags, 1)
			}
		}
	}

	// Sort each node's Parents by ID and align Lags accordingly.
	// graph.Edges is built by iterating a map[string]*Node in Phase 3
	// (probability_engine.go UpdateGraphProbabilities), so edge order — and
	// therefore the order in which parents are appended above — is
	// non-deterministic across runs. Because evaluateSCM feeds Parents in
	// slice order as inputs to n.Func, a different parent order produces a
	// different sum and therefore a different backdoor effect estimate,
	// causing Phase 4 cause ranking to flip between runs.
	// Sorting by ID gives a stable canonical order regardless of map iteration.
	for _, n := range cg.Nodes {
		if len(n.Parents) < 2 {
			continue
		}
		type pl struct {
			parent *phase4.CausalNode
			lag    int
		}
		pairs := make([]pl, len(n.Parents))
		for i, p := range n.Parents {
			lag := 1
			if i < len(n.Lags) {
				lag = n.Lags[i]
			}
			pairs[i] = pl{p, lag}
		}
		sort.Slice(pairs, func(i, j int) bool {
			return pairs[i].parent.ID < pairs[j].parent.ID
		})
		for i, p := range pairs {
			n.Parents[i] = p.parent
			n.Lags[i] = p.lag
		}
	}

	// Second pass: fit linear SCMs now that parents are populated and sorted.
	for _, n := range cg.Nodes {
		parentIDs := make([]string, len(n.Parents))
		for i, p := range n.Parents {
			parentIDs[i] = p.ID
		}
		
		// If dataset is available, use observational data to learn edge weights.
		// Fallback to simple linear if no data is provided.
		var scmFunc SCMFunc
		if dataset != nil && len(dataset.Samples) > 0 {
			scmFunc = FitLinearSCM(parentIDs, n.ID, dataset.Samples)
		} else {
			scmFunc = Linear
		}
		
		n.Func = func(inputs []float64, noise float64) float64 { return scmFunc(inputs, noise) }
	}

	return cg
}

/*
ConvertPhase3ResultToPhase4Dataset converts time-series data from Phase 3
into Phase 4's Dataset format for causal estimation.
*/
func ConvertPhase3ResultToPhase4Dataset(graph *phase3.Graph) *phase4.Dataset {
	if graph == nil {
		return nil
	}

	dataset := &phase4.Dataset{
		Samples: make([]phase4.Sample, 0),
	}

	// Create samples from node time-series and edge series
	// Assumption: all series have same length
	maxLength := 0
	for _, node := range graph.Nodes {
		if len(node.Series) > maxLength {
			maxLength = len(node.Series)
		}
	}

	for _, edge := range graph.Edges {
		if len(edge.SourceSeries) > maxLength {
			maxLength = len(edge.SourceSeries)
		}
		if len(edge.TargetSeries) > maxLength {
			maxLength = len(edge.TargetSeries)
		}
	}

	if maxLength == 0 {
		maxLength = 10 // Fallback
	}

	// Create time-indexed samples
	for t := 0; t < maxLength; t++ {
		sample := make(phase4.Sample)

		// Add node values
		for nodeID, node := range graph.Nodes {
			if t < len(node.Series) {
				sample[nodeID] = node.Series[t]
			} else {
				sample[nodeID] = node.State.Load
			}
		}

		// Add edge-based values
		for _, edge := range graph.Edges {
			if t < len(edge.SourceSeries) {
				sample[edge.From+"_out"] = edge.SourceSeries[t]
			}
			if t < len(edge.TargetSeries) {
				sample[edge.To+"_in"] = edge.TargetSeries[t]
			}
		}

		dataset.Samples = append(dataset.Samples, sample)
	}

	return dataset
}

/*
ConvertPhase4GraphToPhase5Graph converts Phase 4's CausalGraph
to Phase 5's CausalGraph format for agent training.

Note: Both types have identical structure, just in different packages.
*/
func ConvertPhase4GraphToPhase5Graph(
	p4Graph *phase4.CausalGraph,
) *phase5.CausalGraph {
	if p4Graph == nil {
		return nil
	}

	p5Graph := &phase5.CausalGraph{
		Nodes: make(map[string]*phase5.CausalNode),
		Edges: make([]*phase5.CausalEdge, 0),
	}

	// Convert nodes
	nodeMap := make(map[string]*phase5.CausalNode)
	for nodeID, p4Node := range p4Graph.Nodes {
		p5Node := &phase5.CausalNode{
			ID:        p4Node.ID,
			Value:     p4Node.Value,
			Timestamp: p4Node.Timestamp,
			Noise:     p4Node.Noise,
			Parents:   make([]*phase5.CausalNode, 0),
			Lags:      make([]int, 0),
			Func:      p4Node.Func, // Copy the learned structural equation directly
		}
		nodeMap[nodeID] = p5Node
		p5Graph.Nodes[nodeID] = p5Node
	}

	// Convert edges and update parent references
	for _, p4Edge := range p4Graph.Edges {
		if fromNode, exists := nodeMap[p4Edge.From.ID]; exists {
			if toNode, exists := nodeMap[p4Edge.To.ID]; exists {
				p5Edge := &phase5.CausalEdge{
					From: fromNode,
					To:   toNode,
					Lag:  p4Edge.Lag,
				}
				p5Graph.Edges = append(p5Graph.Edges, p5Edge)

				// Update parent references
				toNode.Parents = append(toNode.Parents, fromNode)
				toNode.Lags = append(toNode.Lags, p4Edge.Lag)
			}
		}
	}

	return p5Graph
}

/*
ConvertPhase4ExplanationToPhase5Explanation converts Phase 4's Explanation
to Phase 5's Explanation format (they're identically structured).
*/
func ConvertPhase4ExplanationToPhase5Explanation(
	p4Exp phase4.Explanation,
) phase5.Explanation {
	return phase5.Explanation{
		Causes:      p4Exp.Causes,
		Effects:     p4Exp.Effects,
		Uncertainty: p4Exp.Uncertainty,
	}
}

// ============================================================================
// PHASE 4 → PHASE 5 CONVERTERS (CONTINUATION)
// ============================================================================

/*
ConvertPhase4ExplanationToPhase5BeliefState converts Phase 4's causal
explanation into Phase 5's belief state for agent decision-making.
*/
func ConvertPhase4ExplanationToPhase5BeliefState(
	explanation phase4.Explanation,
	staticValues map[string]float64,
) phase5.BeliefState {

	belief := phase5.BeliefState{
		Mean: make(map[string]float64),
		Var:  make(map[string]float64),
	}

	// Initialize from static values
	for nodeID, val := range staticValues {
		belief.Mean[nodeID] = val
		belief.Var[nodeID] = 0.1 // Small default variance
	}

	// Update with explanation effects
	for cause, effect := range explanation.Effects {
		belief.Mean[cause] = effect
		belief.Var[cause] = explanation.Uncertainty[cause]
	}

	return belief
}

/*
GenerateInterventionActions creates candidate intervention actions
from the causal graph and explanation.
*/
func GenerateInterventionActions(
	explanation phase4.Explanation,
	nodeIDs []string,
) []phase5.Action {

	actions := make([]phase5.Action, 0)

	// Action 1: Reduce primary cause
	if len(explanation.Causes) > 0 {
		primaryCause := explanation.Causes[0]
		actions = append(actions, phase5.Action{
			Interventions: map[string]float64{
				primaryCause: -0.5, // 50% reduction
			},
		})
	}

	// Action 2: Reduce all causes proportionally
	if len(explanation.Causes) > 0 {
		allInterventions := make(map[string]float64)
		for _, cause := range explanation.Causes {
			allInterventions[cause] = -0.3 // 30% each
		}
		actions = append(actions, phase5.Action{
			Interventions: allInterventions,
		})
	}

	// Action 3: No intervention (baseline)
	actions = append(actions, phase5.Action{
		Interventions: make(map[string]float64),
	})

	return actions
}

// ============================================================================
// ERROR HANDLING
// ============================================================================

/*
ValidateConversionPhase3ToPhase4 ensures conversion was successful
*/
func ValidateConversionPhase3ToPhase4(cg *phase4.CausalGraph) error {
	if cg == nil {
		return fmt.Errorf("Phase 4 graph is nil")
	}
	if len(cg.Nodes) == 0 {
		return fmt.Errorf("Phase 4 graph has no nodes")
	}
	if len(cg.Edges) == 0 {
		return fmt.Errorf("Phase 4 graph has no edges")
	}
	return nil
}

/*
ValidateConversionPhase4ToPhase5 ensures belief state is valid
*/
func ValidateConversionPhase4ToPhase5(belief phase5.BeliefState) error {
	if len(belief.Mean) == 0 {
		return fmt.Errorf("Phase 5 belief state has no mean values")
	}
	if len(belief.Var) == 0 {
		return fmt.Errorf("Phase 5 belief state has no variance values")
	}
	return nil
}

// calculateVariance is a helper to compute the variance of a slice.
func calculateVariance(data []float64) float64 {
	if len(data) < 2 {
		return 0.0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	mean := sum / float64(len(data))
	
	sqSum := 0.0
	for _, v := range data {
		diff := v - mean
		sqSum += diff * diff
	}
	return sqSum / float64(len(data)-1)
}
