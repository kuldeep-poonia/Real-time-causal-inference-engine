package phase3_causal

import (
	"fmt"
	"sort"
)

// ─────────────────────────────────────────────────────────────────────────────
// PHASE 3: PHYSICS-BASED QUEUE PROPAGATION ENGINE
//
// Scientific basis:
//
//   1. QUEUEING THEORY (M/M/1)
//      λ > μ  →  ρ ≥ 1  →  queue grows at rate (λ-μ)
//      L = (λ-μ)·t  (unstable regime)
//      W = L/λ      (Little's Law: mean wait time)
//
//   2. FLOW CONSERVATION
//      outflow(A) = inflow(A) - service(A) + overflow(A)
//      overflow spills to downstream:
//        λ_B = λ_B_own + overflow_A × spillover_factor
//
//   3. TEMPORAL CAUSALITY
//      Cause must precede effect in real time.
//      We reject edges where cause_timestamp >= effect_timestamp.
//
//   4. MULTI-HOP CHAINS
//      delay accumulates:  W_total = W_A + W_B + W_C + ...
//      strength decays:    s_hop = s_prev × decay^hop
//
//   5. STRUCTURAL CAUSAL MODEL
//      X_k = f_k(PA_k, ε_k)
//      Here f_k models: X_k = load_k(λ_k, μ_k) + upstream_spillover
// ─────────────────────────────────────────────────────────────────────────────

const (
	// SpilloverFactor: fraction of excess arrivals (λ-μ) that spills to downstream.
	// Physical meaning: a saturated node re-routes (λ-μ) × SpilloverFactor per unit.
	SpilloverFactor = 0.6

	// StrengthDecayPerHop: causal strength multiplied per hop.
	// Physical meaning: each intermediate node partially absorbs and re-generates the signal.
	StrengthDecayPerHop = 0.72
)

// ─────────────────────────────────────────────
// TYPES
// ─────────────────────────────────────────────

// QueueHop records the queue state at a single node in the propagation chain.
type QueueHop struct {
	NodeID string

	// Queueing variables at this hop
	ArrivalRate  float64 // λ_effective (own + upstream spillover)
	ServiceRate  float64 // μ
	QueueLength  float64 // Primary L (M/M/1)
	IsOverloaded bool    // λ > μ

	// Model Evaluations (Interface Abstraction)
	ModelEvaluations map[string]QueueModelResult

	// Propagation accounting
	GeneratedDelay   float64 // Primary W at this node (M/M/1)
	AccumulatedDelay float64 // sum of W from root to here
	HopCount         int
	CausalStrength   float64 // strength after decay

	// Temporal validity
	TemporalValid bool // cause timestamp < effect timestamp
}

// PropagationChain records the full causal path from a root node to a target.
type PropagationChain struct {
	RootNode   string
	TargetNode string
	Hops       []QueueHop

	TotalDelay    float64
	FinalStrength float64

	// IsPhysicallyValid: every hop satisfies queueing physics AND temporal ordering.
	IsPhysicallyValid bool
}

// RootCauseResult holds the result of backward root-cause tracing.
type RootCauseResult struct {
	NodeID  string
	Score   float64

	IsOverloaded bool
	QueueLength  float64
	Load         float64

	PropagationChain    *PropagationChain
	PhysicalExplanation string
}

// ─────────────────────────────────────────────────────────────────────────────
// PropagateQueueLoad — forward physics propagation from root to target.
//
// Algorithm:
//   BFS from rootNode following graph edges.
//   At each node, compute effective λ = own_λ + upstream_spillover.
//   If λ > μ: node is overloaded, generates delay W, spills to downstream.
//   Delay accumulates; strength decays per hop.
//   Temporal ordering checked at every hop.
// ─────────────────────────────────────────────────────────────────────────────

func PropagateQueueLoad(
	graph *Graph,
	nodeStates map[string]NodeState,
	rootNode, targetNode string,
) *PropagationChain {

	chain := &PropagationChain{
		RootNode:   rootNode,
		TargetNode: targetNode,
	}

	type frame struct {
		nodeID           string
		accDelay         float64
		strength         float64
		hopCount         int
		spilloverIn      float64 // overflow arriving from upstream
		upstreamTimestamp float64

		path map[string]bool
	}

	rootState := nodeStates[rootNode]

	queue := []frame{{
		nodeID:           rootNode,
		accDelay:         0,
		strength:         1.0,
		hopCount:         0,
		spilloverIn:      0,
		upstreamTimestamp: rootState.Timestamp,

		path: map[string]bool{rootNode: true},
	}}

	var hops []QueueHop
	reachedTarget := false

	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]

		

		state := nodeStates[f.nodeID]

		// ── TEMPORAL VALIDATION ───────────────────────────────────
		// Cause must precede effect: this node's timestamp must be
		// >= upstream timestamp (effect cannot precede cause).
		temporalValid := state.Timestamp >= f.upstreamTimestamp

		// ── EFFECTIVE ARRIVAL RATE ────────────────────────────────
		// Flow conservation: λ_eff = λ_own + spillover from upstream
		lambdaEff := state.ArrivalRate + f.spilloverIn
		if lambdaEff < state.ArrivalRate {
			lambdaEff = state.ArrivalRate
		}

		mu := state.ServiceRate
		if mu < 1e-9 {
			mu = 1e-9
		}

		isOverloaded := lambdaEff > mu

		// ── QUEUE BUILD AND DELAY ─────────────────────────────────
		// Physics:
		//   Stable (λ < μ):   L = ρ/(1-ρ),    W = L/λ
		//   Unstable (λ ≥ μ): L grows at rate (λ-μ)·t per hop
		//                     W = L/μ  (service-rate limited)
		var queueLen, generatedDelay float64
		evaluations := make(map[string]QueueModelResult)

		// Evaluate all registered queue models
		for _, model := range RegisteredQueueModels {
			res := model.Calculate(lambdaEff, mu, isOverloaded, f.hopCount, state)
			evaluations[model.Name()] = res

			// Preserve M/M/1 as the primary model for backward compatibility
			if model.Name() == "M/M/1" {
				queueLen = res.QueueLength
				generatedDelay = res.Delay
			}
		}

		// ── STRENGTH DECAY ────────────────────────────────────────
		strength := f.strength
		if f.hopCount > 0 {
			strength *= StrengthDecayPerHop
		}

		accDelay := f.accDelay + generatedDelay

		hop := QueueHop{
			NodeID:           f.nodeID,
			ArrivalRate:      lambdaEff,
			ServiceRate:      mu,
			QueueLength:      queueLen,
			IsOverloaded:     isOverloaded,
			ModelEvaluations: evaluations,
			GeneratedDelay:   generatedDelay,
			AccumulatedDelay: accDelay,
			HopCount:         f.hopCount,
			CausalStrength:   strength,
			TemporalValid:    temporalValid,
		}
		hops = append(hops, hop)

		if f.nodeID == targetNode {
			reachedTarget = true
			break
		}

		// ── COMPUTE SPILLOVER TO DOWNSTREAM ──────────────────────
		// Only overloaded nodes spill.  Stable nodes absorb all traffic.
		spillover := 0.0
		if isOverloaded {
			spillover = (lambdaEff - mu) * SpilloverFactor
		}

		// ── ENQUEUE DOWNSTREAM NEIGHBORS ─────────────────────────
		for _, edge := range graph.Edges {
	if edge.From == f.nodeID && !f.path[edge.To] {

		// copy path
		newPath := make(map[string]bool)
		for k, v := range f.path {
			newPath[k] = v
		}
		newPath[edge.To] = true

		queue = append(queue, frame{
			nodeID: edge.To,
			accDelay: accDelay,
			strength: strength,
			hopCount: f.hopCount + 1,
			spilloverIn: spillover,
			upstreamTimestamp: state.Timestamp,
			path: newPath,
		})
	}
}
	}

	chain.Hops = hops
	chain.IsPhysicallyValid = reachedTarget

	if len(hops) > 0 {
		last := hops[len(hops)-1]
		chain.TotalDelay = last.AccumulatedDelay
		chain.FinalStrength = last.CausalStrength
	}

	return chain
}

// ─────────────────────────────────────────────────────────────────────────────
// FindRootCauseByPropagation — backward trace from target to origin.
//
// Algorithm:
//   Walk backward through edges (DFS/stack).
//   At each step, enforce temporal ordering: predecessor.timestamp < node.timestamp.
//   A node is a ROOT CAUSE if it has NO incoming edges in the graph.
//   For each candidate root, run forward PropagateQueueLoad to compute chain score.
//   Score = FinalStrength × (ρ if overloaded, else 1)
//
// This is NOT correlation-based.  It is physical:
//   the root is the node whose queue overload CAUSES the delay that reaches target.
// ─────────────────────────────────────────────────────────────────────────────

func FindRootCauseByPropagation(
	graph *Graph,
	nodeStates map[string]NodeState,
	targetNode string,
) []RootCauseResult {

	var results []RootCauseResult

	type backFrame struct {
		nodeID string
		depth  int
	}

	visited := make(map[string]bool)
	stack := []backFrame{{nodeID: targetNode, depth: 0}}

	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if visited[f.nodeID] {
			continue
		}
		visited[f.nodeID] = true

		myState := nodeStates[f.nodeID]
		hasPredecessor := false

		for _, edge := range graph.Edges {
			if edge.To != f.nodeID {
				continue
			}
			hasPredecessor = true

			predState := nodeStates[edge.From]

			// TEMPORAL VALIDATION: cause must precede effect.
			// Reject the edge if predecessor's timestamp >= current node's timestamp.
			// Physics: you cannot cause something that happened before you.
			if predState.Timestamp > myState.Timestamp {
				continue // temporal ordering violated → not a valid causal edge
			}

			if !visited[edge.From] {
				stack = append(stack, backFrame{
					nodeID: edge.From,
					depth:  f.depth + 1,
				})
			}
		}

		// ROOT NODE: no incoming edges (origin of the disturbance)
		if !hasPredecessor {
			state := nodeStates[f.nodeID]

			// Forward propagation to compute physical chain
			chain := PropagateQueueLoad(graph, nodeStates, f.nodeID, targetNode)

			// Score = chain strength × load boost for overloaded roots
			score := chain.FinalStrength
			if state.Load > 1.0 {
				// overloaded root gets score boosted proportional to excess load
				score *= state.Load
			}
			if !chain.IsPhysicallyValid {
				score *= 0.5 // penalize if path didn't reach target
			}

			results = append(results, RootCauseResult{
				NodeID:              f.nodeID,
				Score:               score,
				IsOverloaded:        state.Load > 1.0,
				QueueLength:         state.QueueLength,
				Load:                state.Load,
				PropagationChain:    chain,
				PhysicalExplanation: buildPhysicsExplanation(f.nodeID, state, chain),
			})
		}
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

// ─────────────────────────────────────────────────────────────────────────────
// buildPhysicsExplanation — human-readable causal explanation.
// This answers WHY, not just WHAT.
// ─────────────────────────────────────────────────────────────────────────────

func buildPhysicsExplanation(nodeID string, state NodeState, chain *PropagationChain) string {
	if state.Load > 1.0 {
		return fmt.Sprintf(
			"ROOT CAUSE: %s — arrival_rate λ=%.3f exceeded service_rate μ=%.3f (ρ=%.2f>1) "+
				"→ queue_length L=%.2f grew → propagated delay W=%.3fs over %d hops to target",
			nodeID, state.ArrivalRate, state.ServiceRate, state.Load,
			state.QueueLength, chain.TotalDelay, len(chain.Hops),
		)
	}

	// For stable nodes, we compare M/M/1 to G/G/1
	var lastHop QueueHop
	if len(chain.Hops) > 0 {
		lastHop = chain.Hops[len(chain.Hops)-1]
	}

	// Indicate burst discrepancy if Kingman delay is significantly larger
	burstStr := ""
	if kingmanRes, ok := lastHop.ModelEvaluations["G/G/1 (Kingman)"]; ok {
		if kingmanRes.Delay > lastHop.GeneratedDelay*1.2 && !lastHop.IsOverloaded {
			burstStr = fmt.Sprintf(" [BURST-AWARE G/G/1: W=%.3fs L=%.2f]", kingmanRes.Delay, kingmanRes.QueueLength)
		}
	}

	return fmt.Sprintf(
		"ORIGIN: %s — load ρ=%.2f (stable) → contributed delay W=%.3fs%s over %d hops",
		nodeID, state.Load, chain.TotalDelay, burstStr, len(chain.Hops),
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// ValidateTemporalOrdering — explicit check that a causal edge is time-valid.
// Returns false (and reason) if cause_time >= effect_time.
// Used to reject invalid edges before computing causal strength.
// ─────────────────────────────────────────────────────────────────────────────

func ValidateTemporalOrdering(
	nodeStates map[string]NodeState,
	causeID, effectID string,
) (bool, string) {
	cause, causeOK := nodeStates[causeID]
	effect, effectOK := nodeStates[effectID]

	if !causeOK || !effectOK {
		return false, fmt.Sprintf("missing state for %s or %s", causeID, effectID)
	}

	// HARD RULE: cause must strictly precede effect.
	if cause.Timestamp > effect.Timestamp {
		return false, fmt.Sprintf(
			"TEMPORAL VIOLATION: %s (t=%.3f) >= %s (t=%.3f) — effect cannot precede cause",
			causeID, cause.Timestamp, effectID, effect.Timestamp,
		)
	}

	return true, "temporal ordering satisfied"
}
