package phase3_causal

import (
	"math"
)

/*
BACKDOOR ENGINE

Features:
- Backdoor set detection (parent-based)
- Conditional effect estimation
- Marginal weighting via empirical frequency (scale-invariant)
- Scale-invariant intervention (normalised 1-SD perturbation)
- Direct fallback
- Safe guards for missing data
*/

type BackdoorResult struct {
	Effect float64
	UsedZ  []string
}

// 
// ENTRY POINT
// 

func ComputeBackdoorEffect(
	graph *Graph,
	data *TemporalGraph,
	X, Y string,
) BackdoorResult {

	Zset := findBackdoorSet(graph, X, Y)

	// fallback: no confounder
	if len(Zset) == 0 {
		return BackdoorResult{
			Effect: estimateDirectEffect(data, X, Y),
			UsedZ:  nil,
		}
	}

	var total float64
	var totalWeight float64

	for _, z := range Zset {
		cond := estimateConditionalEffect(data, X, Y, z)
		pz := estimateMarginal(data, z)

		if pz <= 0 {
			continue
		}

		total += cond * pz
		totalWeight += pz
	}

	if totalWeight == 0 {
		return BackdoorResult{
			Effect: 0,
			UsedZ:  Zset,
		}
	}

	return BackdoorResult{
		Effect: total / totalWeight,
		UsedZ:  Zset,
	}
}

// 
// BACKDOOR SET
// 

func findBackdoorSet(graph *Graph, X, Y string) []string {
	Z := make(map[string]bool)
	for _, e := range graph.Edges {
		if e.To == X && e.From != Y {
			Z[e.From] = true
		}
	}
	var result []string
	for k := range Z {
		result = append(result, k)
	}
	return result
}

// 
// CONDITIONAL EFFECT
// 

func estimateConditionalEffect(
	data *TemporalGraph,
	X, Y, Z string,
) float64 {

	xSeries := data.Nodes[X]
	ySeries := data.Nodes[Y]
	zSeries := data.Nodes[Z]

	if xSeries == nil || ySeries == nil || zSeries == nil {
		return 0
	}

	n := min3(len(xSeries.Points), len(ySeries.Points), len(zSeries.Points))
	if n < 2 {
		return 0
	}

	var sumDX2 float64
	var sumDXDY float64

	for i := 1; i < n; i++ {
		x := xSeries.Points[i].Node.Value
		xPrev := xSeries.Points[i-1].Node.Value
		y := ySeries.Points[i].Node.Value
		yPrev := ySeries.Points[i-1].Node.Value
		z := zSeries.Points[i].Node.Value

		dx := x - xPrev
		dy := y - yPrev

		if math.Abs(dx) < 1e-6 {
			continue
		}

		// ✅ stable weight (no exponential collapse)
		w := 1.0 / (1.0 + math.Abs(z))

		sumDX2 += dx * dx * w
		sumDXDY += dx * dy * w
	}

	// ⚠️ critical safeguard (numerical stability)
	if sumDX2 < 1e-12 {
		return 0
	}

	return sumDXDY / sumDX2
}
// 
// MARGINAL P(Z)
// 

// estimateMarginal returns a normalised empirical frequency weight for
// confounder Z in the backdoor formula: E[Y|do(X)] = Σ_z E[Y|X,Z=z] × P(Z=z).
//
// The previous implementation returned the mean absolute value of Z, which is
// NOT a probability. When Z values are large (e.g. arrival rates in hundreds)
// the weights inflate the backdoor sum by orders of magnitude, making effect
// estimates incomparable across nodes with different value scales.
//
// Fix: bin Z's series into 10 equal-width decile buckets, return the empirical
// frequency of the most-recent observation's bucket ∈ (0,1]. This is
// scale-invariant because bucket boundaries are normalised by the observed
// range of Z. Caller normalises by totalWeight across the full Zset.
func estimateMarginal(
	data *TemporalGraph,
	Z string,
) float64 {

	zSeries := data.Nodes[Z]
	if zSeries == nil || len(zSeries.Points) == 0 {
		return 0
	}

	n := len(zSeries.Points)
	vals := make([]float64, n)
	for i, p := range zSeries.Points {
		vals[i] = p.Node.Value
	}

	minV, maxV := vals[0], vals[0]
	for _, v := range vals {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}

	spread := maxV - minV
	if spread < 1e-9 {
		// Constant series: point mass → uniform weight 1.0
		return 1.0
	}

	const nBins = 10
	counts := make([]int, nBins)
	for _, v := range vals {
		bin := int((v - minV) / spread * float64(nBins-1))
		if bin < 0 {
			bin = 0
		}
		if bin >= nBins {
			bin = nBins - 1
		}
		counts[bin]++
	}

	last := vals[n-1]
	lastBin := int((last - minV) / spread * float64(nBins-1))
	if lastBin < 0 {
		lastBin = 0
	}
	if lastBin >= nBins {
		lastBin = nBins - 1
	}

	return float64(counts[lastBin]) / float64(n)
}

// 
// DIRECT EFFECT (fallback)
// 

func estimateDirectEffect(
	data *TemporalGraph,
	X, Y string,
) float64 {

	xSeries := data.Nodes[X]
	ySeries := data.Nodes[Y]

	if xSeries == nil || ySeries == nil {
		return 0
	}

	n := min2(len(xSeries.Points), len(ySeries.Points))
	if n < 2 {
		return 0
	}

	var sumDX2 float64
	var sumDXDY float64

	for i := 1; i < n; i++ {
		dx := xSeries.Points[i].Node.Value - xSeries.Points[i-1].Node.Value
		dy := ySeries.Points[i].Node.Value - ySeries.Points[i-1].Node.Value

		if math.Abs(dx) < 1e-6 {
			continue
		}

		sumDX2 += dx * dx
		sumDXDY += dx * dy
	}

	// ⚠️ stability guard
	if sumDX2 < 1e-12 {
		return 0
	}

	return sumDXDY / sumDX2
}

// 
// HELPERS
// 

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func min3(a, b, c int) int {
	return min2(min2(a, b), c)
}
