package phase3_causal

import (
	"math"
	"absia/pkg/topology"
)

// PCMCIEngine holds the configuration and topology prior for causal discovery.
type PCMCIEngine struct {
	TopologyManager *topology.Manager
	Alpha           float64 // Significance level for conditional independence (e.g., 0.05)
}

// NewPCMCIEngine creates a new PCMCI engine with the given topology manager.
func NewPCMCIEngine(topo *topology.Manager) *PCMCIEngine {
	return &PCMCIEngine{
		TopologyManager: topo,
		Alpha:           0.05,
	}
}

// fisherZ transforms partial correlation into a Z-statistic.
func fisherZ(pcorr float64, n int, dimZ int) float64 {
	// Bound pcorr to avoid NaNs
	if pcorr >= 1.0 {
		pcorr = 0.999999
	}
	if pcorr <= -1.0 {
		pcorr = -0.999999
	}
	z := 0.5 * math.Log((1+pcorr)/(1-pcorr))
	
	// Standard error for partial correlation is 1 / sqrt(N - |Z| - 3)
	se := 1.0
	dof := float64(n - dimZ - 3)
	if dof > 0 {
		se = 1.0 / math.Sqrt(dof)
	}
	return math.Abs(z / se)
}

// momentaryConditionalIndependence performs the MCI test.
func (e *PCMCIEngine) RunMCI(x, y string, xSeries, ySeries []float64, lag int) (float64, float64) {
	// Basic pearson correlation with lag
	if len(xSeries) <= lag || len(ySeries) <= lag {
		return 0, 1.0 // P-value 1.0 = not significant
	}

	var sumX, sumY, sumXY, sumX2, sumY2 float64
	n := len(ySeries) - lag
	
	for i := lag; i < len(ySeries); i++ {
		xi := xSeries[i-lag]
		yi := ySeries[i]
		
		sumX += xi
		sumY += yi
		sumXY += xi * yi
		sumX2 += xi * xi
		sumY2 += yi * yi
	}
	
	nf := float64(n)
	num := nf*sumXY - sumX*sumY
	den := math.Sqrt((nf*sumX2 - sumX*sumX) * (nf*sumY2 - sumY*sumY))
	
	var corr float64
	if den != 0 {
		corr = num / den
	}
	
	// Convert to Z-statistic
	zStat := fisherZ(corr, n, 1) // assuming 1 condition for MCI
	
	// Approximate p-value (very rough, just for engine integration)
	pVal := math.Exp(-0.717 * zStat - 0.416 * zStat * zStat)
	if pVal > 1.0 {
		pVal = 1.0
	}

	// BAYESIAN PRIOR INTEGRATION
	prior := 0.5
	if e.TopologyManager != nil {
		prior = e.TopologyManager.GetEdgePrior(x, y)
	}
	
	adjustedPVal := pVal * ((1.0 - prior) / prior)
	if adjustedPVal > 1.0 {
		adjustedPVal = 1.0
	}
	
	return corr, adjustedPVal
}

// DiscoverGraph runs the PCMCI algorithm across all nodes to build the dependency graph.
func (e *PCMCIEngine) DiscoverGraph(nodes map[string][]float64, minSamples int) *Graph {
	g := &Graph{
		Nodes: make(map[string]*Node),
		Edges: make([]*Edge, 0),
	}
	
	// Add nodes
	for id, series := range nodes {
		g.Nodes[id] = &Node{
			ID:     id,
			Name:   id,
			Series: series,
			State:  NodeState{Timestamp: 0},
		}
	}
	
	// Run pairwise MCI tests
	nodeIDs := make([]string, 0, len(nodes))
	for id := range nodes {
		nodeIDs = append(nodeIDs, id)
	}
	
	for i := 0; i < len(nodeIDs); i++ {
		for j := 0; j < len(nodeIDs); j++ {
			if i == j {
				continue
			}
			
			x := nodeIDs[i]
			y := nodeIDs[j]
			
			// Test lag 1 for causality direction X -> Y
			corr, adjustedPVal := e.RunMCI(x, y, nodes[x], nodes[y], 1)
			
			// We check the raw prior to decide if it's epistemic
			prior := 0.5
			if e.TopologyManager != nil {
				prior = e.TopologyManager.GetEdgePrior(x, y)
			}

			// If it passes standard statistical test with some correlation
			isMeasured := adjustedPVal < e.Alpha && corr > 0.1
			// If it fails the standard test but has a very strong OTel topological prior
			isInferred := !isMeasured && prior > 0.8
			
			if isMeasured || isInferred {
				source := EdgeSourceObserved
				uncertainty := adjustedPVal
				evidence := "measured_correlation"
				
				if isInferred {
					source = EdgeSourceInferred
					// Uncertainty is high because we didn't measure it directly
					uncertainty = 0.8 
					evidence = "otel_topology_prior"
					// We artificially boost existence prob so the causal builder doesn't drop it
					adjustedPVal = 1.0 - prior 
					// Give it a synthetic minimal strength so it routes
					if corr < 0.05 { corr = 0.05 }
				}

				g.Edges = append(g.Edges, &Edge{
					From:           x,
					To:             y,
					ExistenceProb:  1.0 - adjustedPVal,
					CausalStrength: corr,
					SourceSeries:   nodes[x],
					TargetSeries:   nodes[y],
					Source:         source,
					Uncertainty:    uncertainty,
					EvidenceBasis:  evidence,
				})
			}
		}
	}
	
	return g
}
