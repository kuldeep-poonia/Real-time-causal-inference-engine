package phase3_causal

import (
	"math"
	"math/rand"
	"sort"
)

/*
GRAPH MANAGER (FINAL+++)

additions:
- richer mutation space
- order-independent signatures
- probability floor (numerical stability)
- bounded history
*/



type GraphManager struct {
	Graphs  []*WeightedGraph
	History [][]*WeightedGraph
	rng     *rand.Rand // deterministic, per-instance — never touch the global rand
}

/*
CONFIG
*/
type GraphManagerConfig struct {
	LearningRate float64
	MinProb      float64
	MaxGraphs    int

	ComplexityPenalty float64

	ProbFloor float64
	MaxHistory int
}

// NewGraphManager creates a deterministic graph manager.
// Pass seed=0 for a fixed default seed (reproducible). Pass a non-zero seed to vary.
func NewGraphManager(initial *Graph, seed int64) *GraphManager {
	if seed == 0 {
		seed = 42
	}
	return &GraphManager{
		Graphs: []*WeightedGraph{
			{
				Graph:       initial,
				Probability: 1.0,
			},
		},
		History: [][]*WeightedGraph{},
		rng:     rand.New(rand.NewSource(seed)),
	}
}

/*
UPDATE
*/
func (gm *GraphManager) Update(
	results []InterventionResult,
	cfg GraphManagerConfig,
) {

	// 1. mutation (exploration) — pass rng so mutations are deterministic
	newGraphs := generateNewGraphs(gm.Graphs, gm.rng)
	gm.Graphs = append(gm.Graphs, newGraphs...)

	// 2. evaluation
	for _, wg := range gm.Graphs {

		score := evaluateGraph(wg.Graph, results, cfg)
		score = math.Tanh(score)

		wg.Probability *= math.Exp(cfg.LearningRate * score)

		// smoothing
wg.Probability = 0.7*wg.Probability + 0.3*(1.0/float64(len(gm.Graphs)))

		// probability floor
		if wg.Probability < cfg.ProbFloor {
			wg.Probability = cfg.ProbFloor
		}
	}

	// 3. normalize
	normalize(gm.Graphs)

	// 4. diversity
	gm.Graphs = enforceDiversity(gm.Graphs)

	// 5. prune
	gm.Graphs = pruneGraphs(gm.Graphs, cfg.MinProb)

	// 6. limit
	sort.Slice(gm.Graphs, func(i, j int) bool {
		return gm.Graphs[i].Probability > gm.Graphs[j].Probability
	})

	if len(gm.Graphs) > cfg.MaxGraphs {
		gm.Graphs = gm.Graphs[:cfg.MaxGraphs]
	}

	// 7. history (bounded)
	gm.History = append(gm.History, cloneGraphs(gm.Graphs))

	if len(gm.History) > cfg.MaxHistory {
		gm.History = gm.History[len(gm.History)-cfg.MaxHistory:]
	}
}

/*
RICH MUTATION
*/
func generateNewGraphs(base []*WeightedGraph, rng *rand.Rand) []*WeightedGraph {

	var result []*WeightedGraph

	for _, wg := range base {

		g := wg.Graph

		// random edge remove
		if len(g.Edges) > 0 {
			newG := cloneGraph(g)

			idx := rng.Intn(len(newG.Edges))
			newG.Edges = append(newG.Edges[:idx], newG.Edges[idx+1:]...)

			result = append(result, &WeightedGraph{
				Graph:       newG,
				Probability: wg.Probability * 0.5,
			})
		}

		// random edge flip
		if len(g.Edges) > 0 {
			newG := cloneGraph(g)

			idx := rng.Intn(len(newG.Edges))
			e := newG.Edges[idx]
e.From, e.To = e.To, e.From

// UPDATE SERIES AFTER FLIP
if nodeFrom, ok := newG.Nodes[e.From]; ok {
	e.SourceSeries = nodeFrom.Series
}
if nodeTo, ok := newG.Nodes[e.To]; ok {
	e.TargetSeries = nodeTo.Series
}

			result = append(result, &WeightedGraph{
				Graph:       newG,
				Probability: wg.Probability * 0.5,
			})
		}

		// add new random edge — sort node keys first for determinism
		nodes := keys(g.Nodes)
		sort.Strings(nodes)
		if len(nodes) >= 2 {
			from := nodes[rng.Intn(len(nodes))]
			to := nodes[rng.Intn(len(nodes))]

			if from != to {
				newG := cloneGraph(g)

				newG.Edges = append(newG.Edges, &Edge{
					From:           from,
					To:             to,
					ExistenceProb:  0.5,
					CausalStrength: 0.1,
					Variance:       1.0,


					SourceSeries: newG.Nodes[from].Series,
	TargetSeries: newG.Nodes[to].Series,
				})

				result = append(result, &WeightedGraph{
					Graph:       newG,
					Probability: wg.Probability * 0.5,
				})
			}
		}
	}

	return result
}

/*
EVALUATION
*/
func evaluateGraph(
	graph *Graph,
	results []InterventionResult,
	cfg GraphManagerConfig,
) float64 {

	var score float64

	for _, r := range results {

		for _, e := range graph.Edges {

			if e.From == r.From && e.To == r.To {

				// ✅ ADD THIS ABOVE contrib
temporal := computeTemporalCausality(e.SourceSeries, e.TargetSeries)

// ✅ NEW contrib
contrib := e.ExistenceProb *
	r.Effect *
	(1.0 - e.Variance) *
	temporal

				if r.Valid {
					score += contrib
				} else {
					score -= contrib
				}
			}
		}
	}

	score -= cfg.ComplexityPenalty * float64(len(graph.Edges))

	return score
}

/*
SIGNATURE (ORDER-INDEPENDENT)
*/
func graphSignature(g *Graph) string {

	var edges []string

	for _, e := range g.Edges {
		edges = append(edges, e.From+"->"+e.To)
	}

	sort.Strings(edges)

	return join(edges)
}

func join(arr []string) string {
	s := ""
	for _, a := range arr {
		s += a + ";"
	}
	return s
}

/*
DIVERSITY
*/
func enforceDiversity(graphs []*WeightedGraph) []*WeightedGraph {

	seen := make(map[string]bool)
	var result []*WeightedGraph

	for _, g := range graphs {
		key := graphSignature(g.Graph)

		if !seen[key] {
			seen[key] = true
			result = append(result, g)
		}
	}

	return result
}

/*
NORMALIZE
*/
func normalize(graphs []*WeightedGraph) {

	var total float64

	// ✅ FIRST sum
	for _, g := range graphs {
		total += g.Probability
	}

	// ✅ THEN check
	if total < 1e-9 {
		return
	}

	// normalize
	for _, g := range graphs {
		g.Probability /= total
	}
}
/*
PRUNE
*/
func pruneGraphs(graphs []*WeightedGraph, minProb float64) []*WeightedGraph {

	var result []*WeightedGraph

	for _, g := range graphs {
		if g.Probability >= minProb {
			result = append(result, g)
		}
	}

	return result
}

/*
HELPERS
*/
func cloneGraph(g *Graph) *Graph {

	newG := &Graph{
		Nodes:   make(map[string]*Node),
		Edges:   []*Edge{},
		Factors: g.Factors,
	}

	for k, v := range g.Nodes {
	nodeCopy := *v
	newG.Nodes[k] = &nodeCopy
}

	for _, e := range g.Edges {
		copy := *e
		newG.Edges = append(newG.Edges, &copy)
	}

	return newG
}

func cloneGraphs(graphs []*WeightedGraph) []*WeightedGraph {

	var result []*WeightedGraph

	for _, g := range graphs {
		result = append(result, &WeightedGraph{
			Graph:       cloneGraph(g.Graph),
			Probability: g.Probability,
		})
	}

	return result
}

func keys(m map[string]*Node) []string {
	k := make([]string, 0, len(m))
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k) // deterministic node ordering for mutations
	return k
}