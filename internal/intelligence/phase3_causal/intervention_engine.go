package phase3_causal

import (
	"math"
	"time"
)

type InterventionConfig struct {
	DeltaFactor float64

	MinEffect float64
	Epsilon   float64

	Boost float64
	Decay float64

	TimeTolerance time.Duration
	LagSteps      []int
}

type InterventionResult struct {
	From string
	To   string

	Effect float64
	Valid  bool
}

func RunIntervention(
	graph *Graph,
	data *TemporalGraph,
	cfg InterventionConfig,
) []InterventionResult {

	var results []InterventionResult

	nodeStates := make(map[string]NodeState)
	for id, node := range graph.Nodes {
		nodeStates[id] = node.State
	}

	for _, edge := range graph.Edges {

		source := data.Nodes[edge.From]
		target := data.Nodes[edge.To]

		if source == nil || target == nil {
			continue
		}

		effectAB := estimateInterventionEffect(source, target, cfg)
		effectBA := estimateInterventionEffect(target, source, cfg)

		finalEffect := effectAB
		valid := false

		if math.Abs(effectAB) > math.Abs(effectBA) &&
			math.Abs(effectAB) >= cfg.MinEffect {

			modGraph, modStates := SimulateNodeIntervention(graph, nodeStates, edge.From, edge.To, 0.5)

// BASELINE
base := PropagateQueueLoad(graph, nodeStates, edge.From, edge.To)
baseDelay := base.TotalDelay

// AFTER DO-OPERATOR
after := PropagateQueueLoad(modGraph, modStates, edge.From, edge.To)
afterDelay := after.TotalDelay

// VALIDATION
valid = (afterDelay < baseDelay) || (baseDelay == 0 && math.Abs(effectAB) >= cfg.MinEffect)

			if valid {
				edge.ExistenceProb *= cfg.Boost
				edge.ExistenceProb = clamp(edge.ExistenceProb, 0, 1)

				edge.Variance *= 0.8
			} else {
				edge.ExistenceProb *= cfg.Decay
				edge.ExistenceProb = clamp(edge.ExistenceProb, 0, 1)

				edge.Variance *= 1.2
			}
		} else {
			edge.ExistenceProb *= cfg.Decay
			edge.ExistenceProb = clamp(edge.ExistenceProb, 0, 1)

			edge.Variance *= 1.2
		}

		results = append(results, InterventionResult{
			From:   edge.From,
			To:     edge.To,
			Effect: finalEffect,
			Valid:  valid,
		})
	}

	return results
}

func SimulateNodeIntervention(
	graph *Graph,
	nodeStates map[string]NodeState,
	candidateNode, targetNode string,
	reductionFactor float64,
) (*Graph, map[string]NodeState) {

	// STEP 1: CLONE GRAPH
	modGraph := cloneGraph(graph)

	// STEP 2: REMOVE ALL INCOMING EDGES (DO-OPERATOR)
	var newEdges []*Edge
	for _, e := range modGraph.Edges {
		if e.To != candidateNode {
			newEdges = append(newEdges, e)
		}
	}
	modGraph.Edges = newEdges

	// STEP 3: COPY STATES
	modStates := make(map[string]NodeState, len(nodeStates))
	for k, v := range nodeStates {
		modStates[k] = v
	}

	// STEP 4: FORCE INTERVENTION VALUE
	state := modStates[candidateNode]

// ❗ COMPLETE ISOLATION
state.ArrivalRate = state.ServiceRate * reductionFactor

// reset load
state.Load = state.ArrivalRate / state.ServiceRate

modStates[candidateNode] = state
	state.Load = state.ArrivalRate / state.ServiceRate
	modStates[candidateNode] = state

	return modGraph, modStates
}

func estimateInterventionEffect(
	source *TemporalSeries,
	target *TemporalSeries,
	cfg InterventionConfig,
) float64 {

	var totalEffect float64
	var totalWeight float64

	for _, lag := range cfg.LagSteps {

		pairs := alignWithLagForIntervention(source, target, lag, cfg.TimeTolerance)

		if len(pairs) < 2 {
			continue
		}

		std := computeStd(pairs)
		delta := cfg.DeltaFactor * std

		var lagEffect float64
		var count float64

		for i := 1; i < len(pairs); i++ {

			dx := pairs[i].X - pairs[i-1].X
			dy := pairs[i].Y - pairs[i-1].Y

			if math.Abs(dx) < cfg.Epsilon {
				continue
			}

			effect := dy / (dx + cfg.Epsilon)

			weight := math.Exp(-math.Abs(dx - delta))

			lagEffect += effect * weight
			count += weight
		}

		if count == 0 {
			continue
		}

		avgEffect := lagEffect / count

		w := math.Abs(avgEffect)

		totalEffect += avgEffect * w
		totalWeight += w
	}

	if totalWeight == 0 {
		return 0
	}

	return totalEffect / totalWeight
}

type IVPair struct {
	X float64
	Y float64
}

func alignWithLagForIntervention(
	a, b *TemporalSeries,
	lag int,
	tol time.Duration,
) []IVPair {

	var result []IVPair

	pointsA := a.Points
	pointsB := b.Points

	for i := 0; i < len(pointsA); i++ {

		j := i + lag
		if j >= len(pointsB) {
			continue
		}

		tA := pointsA[i].Time
		tB := pointsB[j].Time

		if absDuration(tA.Sub(tB)) > tol {
			continue
		}

		result = append(result, IVPair{
			X: pointsA[i].Node.Value,
			Y: pointsB[j].Node.Value,
		})
	}

	return result
}

func computeStd(pairs []IVPair) float64 {

	if len(pairs) == 0 {
		return 0
	}

	var sum float64
	for _, p := range pairs {
		sum += p.X
	}

	mean := sum / float64(len(pairs))

	var variance float64
	for _, p := range pairs {
		diff := p.X - mean
		variance += diff * diff
	}

	variance /= float64(len(pairs))

	return math.Sqrt(variance)
}

func clamp(x, min, max float64) float64 {
	if x < min {
		return min
	}
	if x > max {
		return max
	}
	return x
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}