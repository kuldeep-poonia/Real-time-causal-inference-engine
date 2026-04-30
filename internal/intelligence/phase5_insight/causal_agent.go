package phase5_insight

import (
	"math"
	"math/rand"
	"sort"
)

/*
Phase 5 — Causal RL Agent
*/

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
	From *CausalNode
	To   *CausalNode
	Lag  int
}

type CausalGraph struct {
	Nodes map[string]*CausalNode
	Edges []*CausalEdge
}

/* ===========================
   PHASE 4 INTERFACE
=========================== */

type Explanation struct {
	Causes      []string
	Effects     map[string]float64
	Uncertainty map[string]float64
}

/* ===========================
   BELIEF
=========================== */

type BeliefState struct {
	Mean map[string]float64
	Var  map[string]float64
}

/* ===========================
   ACTION
=========================== */

type Action struct {
	Interventions map[string]float64
}

/* ===========================
   FEATURES
=========================== */

// features builds the policy input vector from belief state and explanation.
//
// CRITICAL: Go map iteration order is non-deterministic. Sort node keys
// alphabetically before appending to guarantee a stable, reproducible
// feature vector layout. Weight W[i][j] must always refer to the same
// feature regardless of map insertion order.
func features(b BeliefState, exp Explanation) []float64 {
	f := []float64{}

	bKeys := make([]string, 0, len(b.Mean))
	for k := range b.Mean {
		bKeys = append(bKeys, k)
	}
	sort.Strings(bKeys)
	for _, k := range bKeys {
		f = append(f, b.Mean[k])
		f = append(f, b.Var[k])
	}

	// exp.Causes is already sorted by Phase 4 (by effect magnitude).
	for _, c := range exp.Causes {
		f = append(f, exp.Effects[c])
		f = append(f, exp.Uncertainty[c])
	}
	return f
}

/* ===========================
   POLICY
=========================== */

type Policy struct {
	W [][]float64
	B []float64
}

func (p *Policy) logits(b BeliefState, exp Explanation, actions []Action) []float64 {
	x := features(b, exp)
	out := make([]float64, len(actions))

	for i, a := range actions {
		sum := 0.0
		for j := 0; j < len(x) && j < len(p.W[i]); j++ {
			sum += p.W[i][j] * x[j]
		}

		// interaction-aware contribution
		for n1 := range a.Interventions {
			for n2 := range a.Interventions {
				sum += exp.Effects[n1] * exp.Effects[n2]
			}
			sum -= exp.Uncertainty[n1]
		}

		out[i] = sum + p.B[i]
	}
	return out
}

func softmax(logits []float64) []float64 {
	maxv := logits[0]
	for _, v := range logits {
		if v > maxv {
			maxv = v
		}
	}
	exp := make([]float64, len(logits))
	sum := 0.0
	for i, v := range logits {
		e := math.Exp(v - maxv)
		exp[i] = e
		sum += e
	}
	for i := range exp {
		exp[i] /= sum
	}
	return exp
}

func (p *Policy) Prob(b BeliefState, exp Explanation, actions []Action) []float64 {
	return softmax(p.logits(b, exp, actions))
}

/* ===========================
   SCM CORE
=========================== */

// topo returns a topological ordering of the CausalGraph using Kahn's algorithm.
// Node keys are sorted at each step for deterministic output.
func topo(graph *CausalGraph) []*CausalNode {
	inDeg := make(map[string]int, len(graph.Nodes))
	for id := range graph.Nodes {
		inDeg[id] = 0
	}
	for _, e := range graph.Edges {
		inDeg[e.To.ID]++
	}

	queue := make([]*CausalNode, 0, len(graph.Nodes))
	for id, d := range inDeg {
		if d == 0 {
			queue = append(queue, graph.Nodes[id])
		}
	}
	sort.Slice(queue, func(i, j int) bool { return queue[i].ID < queue[j].ID })

	order := make([]*CausalNode, 0, len(graph.Nodes))
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		order = append(order, n)

		var nexts []*CausalNode
		for _, e := range graph.Edges {
			if e.From.ID == n.ID {
				inDeg[e.To.ID]--
				if inDeg[e.To.ID] == 0 {
					nexts = append(nexts, graph.Nodes[e.To.ID])
				}
			}
		}
		sort.Slice(nexts, func(i, j int) bool { return nexts[i].ID < nexts[j].ID })
		queue = append(queue, nexts...)
	}

	// Cycle guard: append remaining nodes so callers never crash.
	if len(order) < len(graph.Nodes) {
		remaining := make([]*CausalNode, 0)
		for id, node := range graph.Nodes {
			if inDeg[id] > 0 {
				remaining = append(remaining, node)
			}
		}
		sort.Slice(remaining, func(i, j int) bool { return remaining[i].ID < remaining[j].ID })
		order = append(order, remaining...)
	}
	return order
}

func evaluate(graph *CausalGraph) map[string]float64 {
	vals := map[string]float64{}
	for _, n := range topo(graph) {
		in := []float64{}
		for _, p := range n.Parents {
			in = append(in, vals[p.ID])
		}
		vals[n.ID] = n.Func(in, n.Noise)
	}
	return vals
}

func abduct(graph *CausalGraph, b BeliefState) map[string]float64 {
	U := map[string]float64{}
	for _, n := range topo(graph) {
		in := []float64{}
		for _, p := range n.Parents {
			in = append(in, b.Mean[p.ID])
		}
		base := n.Func(in, 0)
		U[n.ID] = b.Mean[n.ID] - base
	}
	return U
}

func intervene(graph *CausalGraph, a Action) *CausalGraph {
	g := clone(graph)
	for id, val := range a.Interventions {
		n := g.Nodes[id]
		n.Func = func(inputs []float64, noise float64) float64 {
			return val
		}
	}
	return g
}

func counterfactual(graph *CausalGraph, b BeliefState, a Action) map[string]float64 {
	U := abduct(graph, b)
	g := intervene(graph, a)
	for id, n := range g.Nodes {
		n.Noise = U[id]
	}
	return evaluate(g)
}

/* ===========================
   IDENTIFIABILITY (GRAPH BASED)
=========================== */

func identifiable(graph *CausalGraph, exp Explanation, a Action, target string) bool {
	for node := range a.Interventions {
		if _, ok := exp.Effects[node]; !ok {
			return false
		}
		for _, e := range graph.Edges {
			if e.From.ID == target && e.To.ID == node {
				return false
			}
		}
	}
	return true
}

/* ===========================
   CAUSAL CREDIT
=========================== */

func causalCredit(graph *CausalGraph, b BeliefState, a Action, target string) float64 {
	cf := counterfactual(graph, b, a)
	base := b.Mean[target]
	return cf[target] - base
}

/* ===========================
   BELIEF UPDATE (BAYESIAN)
=========================== */

func updateBelief(b BeliefState, obs map[string]float64) BeliefState {
	next := BeliefState{Mean: map[string]float64{}, Var: map[string]float64{}}

	for k := range b.Mean {
		priorMean := b.Mean[k]
		priorVar := b.Var[k]
		obsVar := 0.05

		K := priorVar / (priorVar + obsVar)

		next.Mean[k] = priorMean + K*(obs[k]-priorMean)
		next.Var[k] = (1 - K) * priorVar
	}
	return next
}

/* ===========================
   REWARD
=========================== */

func reward(graph *CausalGraph, b BeliefState, a Action, exp Explanation, target string) float64 {
	credit := causalCredit(graph, b, a, target)

	unc := 0.0
	for node := range a.Interventions {
		unc += exp.Uncertainty[node]
	}

	return credit - 0.5*unc
}

/* ===========================
   EXPLORATION (INFO GAIN)
=========================== */

func explorationBonus(exp Explanation, a Action) float64 {
	bonus := 0.0
	for node := range a.Interventions {
		bonus += exp.Uncertainty[node]
	}
	return bonus
}

/* ===========================
   SAMPLE
=========================== */

func sample(probs []float64, actions []Action, rng *rand.Rand) Action {
	r := rng.Float64()
	cum := 0.0
	for i, p := range probs {
		cum += p
		if r < cum {
			return actions[i]
		}
	}
	return actions[len(actions)-1]
}

/* ===========================
   ROLLOUT
=========================== */

type Transition struct {
	S   BeliefState
	A   Action
	R   float64
	Exp Explanation
}

func rollout(graph *CausalGraph, p *Policy, b BeliefState, exp Explanation, actions []Action, target string, horizon int, rng *rand.Rand) []Transition {
	traj := []Transition{}
	curr := b

	for t := 0; t < horizon; t++ {
		probs := p.Prob(curr, exp, actions)
		a := sample(probs, actions, rng)

		if !identifiable(graph, exp, a, target) {
			continue
		}

		cf := counterfactual(graph, curr, a)
		next := updateBelief(curr, cf)

		r := reward(graph, curr, a, exp, target) + explorationBonus(exp, a)

		traj = append(traj, Transition{
			S:   curr,
			A:   a,
			R:   r,
			Exp: exp,
		})

		curr = next
	}
	return traj
}

/* ===========================
   UPDATE (CAUSAL POLICY GRADIENT)
=========================== */

func update(p *Policy, traj []Transition, actions []Action, graph *CausalGraph, target string, alpha float64) {
	for _, tr := range traj {
		x := features(tr.S, tr.Exp)
		logits := p.logits(tr.S, tr.Exp, actions)
		probs := softmax(logits)

		for i := range actions {
			grad := 0.0
			if equalAction(actions[i], tr.A) {
				grad = 1 - probs[i]
			} else {
				grad = -probs[i]
			}

			cg := causalCredit(graph, tr.S, actions[i], target)

			for j := 0; j < len(x) && j < len(p.W[i]); j++ {
				p.W[i][j] += alpha * cg * grad * x[j]
			}
		}
	}
}

func equalAction(a, b Action) bool {
	if len(a.Interventions) != len(b.Interventions) {
		return false
	}
	for k, v := range a.Interventions {
		if b.Interventions[k] != v {
			return false
		}
	}
	return true
}

/* ===========================
   TRAIN
=========================== */

// Train runs causal policy gradient over `episodes` rollouts.
// seed=0 defaults to 42 for reproducibility; pass a non-zero seed to vary.
func Train(graph *CausalGraph, init BeliefState, exp Explanation, actions []Action, target string, episodes int) *Policy {
	return TrainWithSeed(graph, init, exp, actions, target, episodes, 42)
}

// TrainWithSeed is the seeded variant — use in tests or when a reproducible
// policy is required. Always starts from random weight initialisation.
func TrainWithSeed(graph *CausalGraph, init BeliefState, exp Explanation, actions []Action, target string, episodes int, seed int64) *Policy {
	return WarmstartTrain(nil, graph, init, exp, actions, target, episodes, seed)
}

// WarmstartTrain trains the policy, optionally starting from previously
// persisted weights (prior) rather than random initialisation.
//
// When prior is non-nil and its dimensions are compatible with the current
// graph (same number of actions, same feature dimension), the prior weights
// are deep-copied and used as the starting point. This lets the policy
// accumulate knowledge across successive pipeline runs instead of
// re-learning from scratch on every request.
//
// When prior is nil, or when dimensions are incompatible (graph topology
// changed), the function falls back to Xavier-like random initialisation.
func WarmstartTrain(prior *Policy, graph *CausalGraph, init BeliefState, exp Explanation, actions []Action, target string, episodes int, seed int64) *Policy {
	rng := rand.New(rand.NewSource(seed))

	fusion := FuseCausalResults(graph, "", exp.Causes, target)
	exp.Causes = fusion.RootCauses

	// Stable feature dimension: 2×|belief keys| + 2×|causes|.
	bKeys := make([]string, 0, len(init.Mean))
	for k := range init.Mean {
		bKeys = append(bKeys, k)
	}
	sort.Strings(bKeys)
	numFeatures := len(bKeys)*2 + len(exp.Causes)*2

	p := &Policy{
		W: make([][]float64, len(actions)),
		B: make([]float64, len(actions)),
	}

	// Warmstart: use prior weights when dimensions are compatible.
	priorUsable := prior != nil &&
		len(prior.W) == len(actions) &&
		len(actions) > 0 &&
		len(prior.W[0]) == numFeatures

	for i := range actions {
		p.W[i] = make([]float64, numFeatures)
		if priorUsable {
			// Deep copy prior row; avoids aliasing across calls.
			copy(p.W[i], prior.W[i])
			p.B[i] = prior.B[i]
		} else {
			// Xavier-like small random init using the instance rng.
			for j := range p.W[i] {
				p.W[i][j] = (rng.Float64() - 0.5) * 0.01
			}
		}
	}

	for i := 0; i < episodes; i++ {
		traj := rollout(graph, p, init, exp, actions, target, 10, rng)
		update(p, traj, actions, graph, target, 0.001)
	}
	return p
}

/* ===========================
   CLONE
=========================== */

func clone(graph *CausalGraph) *CausalGraph {
	nodes := map[string]*CausalNode{}

	for id, n := range graph.Nodes {
		nodes[id] = &CausalNode{
			ID:        n.ID,
			Value:     n.Value,
			Func:      n.Func,
			Timestamp: n.Timestamp,
			Lags:      append([]int{}, n.Lags...),
		}
	}

	for id, n := range graph.Nodes {
		for _, p := range n.Parents {
			nodes[id].Parents = append(nodes[id].Parents, nodes[p.ID])
		}
	}

	edges := []*CausalEdge{}
	for _, e := range graph.Edges {
		edges = append(edges, &CausalEdge{
			From: nodes[e.From.ID],
			To:   nodes[e.To.ID],
			Lag:  e.Lag,
		})
	}

	return &CausalGraph{Nodes: nodes, Edges: edges}
}
