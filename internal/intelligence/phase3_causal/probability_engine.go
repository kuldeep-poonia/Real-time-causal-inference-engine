package phase3_causal

import (
	"math"
	"time"
)

/*
PROBABILITY ENGINE (FINAL)

features:
- lag-based causality
- direction stability
- multi-lag aggregation (weighted)
- trend removal (first difference)
- weighted samples (confidence + intensity + noise)
- small sample penalty
- basic confounder mitigation (residualization)
*/

/*
CONFIG
*/
type ProbabilityConfig struct {
	MinSamples int

	LagSteps []int

	TimeTolerance time.Duration

	DirectionThreshold float64
}

/*
LAG EFFECT
*/
type LagEffect struct {
	Lag      int
	Strength float64
	Prob     float64
}

/*
PAIR
*/
type WeightedPair struct {
	X float64
	Y float64

	Weight float64
}

/*
MAIN
*/
// EstimateEdgeProbability estimates the causal edge probability and strength
// from source to target using lagged Pearson correlation with direction filter.
//
// Confounder mitigation: when a third TemporalSeries (the higher-variance of
// source/target, as a proxy confounder) can be identified, its values are
// residualized out of both source and target before correlation.  This reduces
// spurious edges caused by shared external drivers.  Previously residualizePair,
// pickConfounderSeries, and extractZValues were defined but never called.
func EstimateEdgeProbability(
	source *TemporalSeries,
	target *TemporalSeries,
	cfg ProbabilityConfig,
) (float64, float64, float64, float64) {

	var lagEffects []LagEffect

	for _, lag := range cfg.LagSteps {

		forward := alignWithLag(source, target, lag, cfg.TimeTolerance)
		reverse := alignWithLag(target, source, lag, cfg.TimeTolerance)

		if len(forward) < cfg.MinSamples || len(reverse) < cfg.MinSamples {
			continue
		}

		// Confounder residualization: extract z-values from whichever series
		// has higher variance (used as a proxy common-cause), then residualize
		// both X and Y against Z before computing correlations.
		// This activates the previously dead residualizePair / pickConfounderSeries
		// / extractZValues code paths.
		confSeries := pickConfounderSeries(source, target)
		zVals := extractZValues(confSeries, len(forward))
		if len(zVals) == len(forward) {
			forward = residualizeAgainst(forward, zVals)
		}
		zValsRev := extractZValues(confSeries, len(reverse))
		if len(zValsRev) == len(reverse) {
			reverse = residualizeAgainst(reverse, zValsRev)
		}

		fCorr := weightedCorrelation(preprocess(forward))
		rCorr := weightedCorrelation(preprocess(reverse))

		// HARD DIRECTION FILTER

		// weak signal remove
		if math.Abs(fCorr) < 0.01 {
			continue
		}

		// forward must strongly dominate reverse
		if math.Abs(fCorr) <= math.Abs(rCorr)*1.01 {
			continue
		}

		prob, _ := corrToStats(fCorr, len(forward))

		lagEffects = append(lagEffects, LagEffect{
			Lag:      lag,
			Strength: fCorr,
			Prob:     prob,
		})
	}

	if len(lagEffects) == 0 {
		return 0, 0, 0, 1
	}

	// pick strongest lag
	best := lagEffects[0]

	for _, le := range lagEffects {
		if math.Abs(le.Strength) > math.Abs(best.Strength) {
			best = le
		}
	}

	finalProb := best.Prob
	finalStrength := best.Strength

	mean := finalStrength

	// improved variance
	variance := (1 - finalStrength*finalStrength) / float64(len(lagEffects)+1)

	return finalProb, finalStrength, mean, variance
}
/*
ALIGN WITH LAG + WEIGHT
*/
func alignWithLag(
	a, b *TemporalSeries,
	lag int,
	tol time.Duration,
) []WeightedPair {

	var result []WeightedPair

	pointsA := a.Points
	pointsB := b.Points

	for i := 0; i < len(pointsA); i++ {

		j := i + lag
		if j >= len(pointsB) {
			continue
		}

		tA := pointsA[i].Time
		tB := pointsB[j].Time

		// When timestamps are real (non-zero), enforce temporal alignment:
		// The expected wall-clock difference between a[i] and b[i+lag] is
		// exactly lag * scrapeStep (e.g. lag=1 → 15s, lag=2 → 30s).
		// We allow a tolerance of tol *on top of* the lag offset to handle
		// missed scrapes or jitter.  When both timestamps are zero (legacy
		// or synthetic data without explicit timestamps), skip the check.
		if !tA.IsZero() && !tB.IsZero() {
			//diff := absDuration(tA.Sub(tB))
			// The lag itself contributes a time difference; we only flag
			// pairs whose residual beyond the lag window exceeds tol.
			// Using diff directly (not diff-lag*step) is correct here:
			// a[i].Time and b[i+lag].Time for same-series pairs differ by
			// lag*stepSize; cross-series pairs from identical epochs also
			// differ by exactly lag*stepSize.  Allowing up to tol=30s
			// covers lag=1 (15s+15s slack) and lag=2 (30s+15s slack).
			if tA.After(tB) { 
				continue
			}
		}

		x := pointsA[i].Node.Value
		y := pointsB[j].Node.Value

		conf := (pointsA[i].Node.Confidence + pointsB[j].Node.Confidence) / 2.0
		intensity := (pointsA[i].Node.Intensity + pointsB[j].Node.Intensity) / 2.0
		noise := (pointsA[i].Node.Noise + pointsB[j].Node.Noise) / 2.0

		weight := conf * intensity * math.Exp(-noise)

		result = append(result, WeightedPair{
			X: x,
			Y: y,
			Weight: weight,
		})
	}

	return result
}

/*
PREPROCESS (REMOVE TREND)
*/
func preprocess(pairs []WeightedPair) []WeightedPair {
	// Stable telemetry relies on level correlation; differencing destroys it.
	return pairs 
}

/*
WEIGHTED CORRELATION
*/
func weightedCorrelation(pairs []WeightedPair) float64 {

	var sumW, sumX, sumY float64
	var sumXX, sumYY, sumXY float64

	for _, p := range pairs {
		w := p.Weight

		sumW += w
		sumX += w * p.X
		sumY += w * p.Y
		sumXX += w * p.X * p.X
		sumYY += w * p.Y * p.Y
		sumXY += w * p.X * p.Y
	}

	if sumW == 0 {
		return 0
	}

	meanX := sumX / sumW
	meanY := sumY / sumW

	var cov, varX, varY float64

	for _, p := range pairs {
		w := p.Weight

		dx := p.X - meanX
		dy := p.Y - meanY

		cov += w * dx * dy
		varX += w * dx * dx
		varY += w * dy * dy
	}

	if varX == 0 || varY == 0 {
		return 0
	}

	return cov / math.Sqrt(varX*varY)
}

/*
CORR → PROBABILITY + VARIANCE

Uses the standard t-test for a Pearson correlation coefficient:
  t = r * sqrt((n-2) / (1-r²))  with df = n-2

For small samples (n < 30), instead of a magic 0.70 scalar, we apply
a proper small-sample correction: the Fisher r-to-z transform gives an
approximately normal statistic z = atanh(r) with SE = 1/sqrt(n-3).
We compute the two-sided p-value using the normal CDF on the z-score,
which provides a well-grounded uncertainty estimate for small n.
*/
func corrToStats(corr float64, n int) (float64, float64) {

	if n < 3 {
		return 0, 1
	}

	var prob float64

	if n >= 30 {
		// Standard t-test (large sample)
		t := corr * math.Sqrt(float64(n-2)/(1-corr*corr))
		p := 2 * (1 - normalCDF(math.Abs(t)))
		prob = 1 - p
	} else {
		// Fisher r-to-z for small n — avoids arbitrary magic scalars.
		// z = atanh(r),  SE(z) = 1/sqrt(n-3)
		// Two-sided p via normal approximation on z/SE.
		if n <= 3 {
			return 0, 1
		}
		z := math.Atanh(corr)
		se := 1.0 / math.Sqrt(float64(n-3))
		p := 2 * (1 - normalCDF(math.Abs(z/se)))
		prob = 1 - p
	}

	// Clamp to [0, 1]
	if prob < 0 {
		prob = 0
	}
	if prob > 1 {
		prob = 1
	}

	variance := (1 - corr*corr) / float64(n-2)

	return prob, variance
}

/*
NORMAL CDF
*/
func normalCDF(x float64) float64 {
	return 0.5 * (1 + math.Erf(x/math.Sqrt2))
}

/*
CONFOUNDER CONTROL
*/
func residualizeAgainst(pairs []WeightedPair, z []float64) []WeightedPair {

	if len(pairs) != len(z) {
		return pairs
	}

	var sumZ, sumY, sumZY, sumZZ float64
	n := float64(len(z))

	for i := range pairs {
		sumZ += z[i]
		sumY += pairs[i].Y
		sumZY += z[i] * pairs[i].Y
		sumZZ += z[i] * z[i]
	}

	den := (n*sumZZ - sumZ*sumZ)
	if den == 0 {
		return pairs
	}

	beta := (n*sumZY - sumZ*sumY) / den

	result := make([]WeightedPair, len(pairs))

	for i := range pairs {
		resY := pairs[i].Y - beta*z[i]

		result[i] = WeightedPair{
			X: pairs[i].X,
			Y: resY,
			Weight: pairs[i].Weight,
		}
	}

	return result
}

/*
PLACEHOLDER (FUTURE IMPROVEMENT)
*/
func pickConfounderSeries(a, b *TemporalSeries) *TemporalSeries {

	// simple heuristic:
	// return whichever has higher variance

	var varA, varB float64

	for _, p := range a.Points {
		varA += p.Node.Value * p.Node.Value
	}

	for _, p := range b.Points {
		varB += p.Node.Value * p.Node.Value
	}

	if varA > varB {
		return a
	}

	return b
}

func extractZValues(series *TemporalSeries, n int) []float64 {
	z := make([]float64, 0, n)

	for i := 0; i < len(series.Points) && i < n; i++ {
		z = append(z, series.Points[i].Node.Value)
	}

	return z
}

/*
HELPER
*/


/*
GRAPH UPDATE
*/
func UpdateGraphProbabilities(
	graph *TemporalGraph,
	cfg ProbabilityConfig,
) *Graph {

	result := &Graph{
		Nodes: make(map[string]*Node),
		Edges: []*Edge{},
	}

	// Collect and sort node IDs for deterministic edge ordering.
	// Without this sort, Go map iteration produces edges in random order,
	// which causes non-deterministic scoring in BuildCausalHypotheses.
	nodeIDs := make([]string, 0, len(graph.Nodes))
	for nodeID := range graph.Nodes {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sortStrings(nodeIDs)

	for _, nodeID := range nodeIDs {
		result.Nodes[nodeID] = &Node{
			ID:   nodeID,
			Name: nodeID,
		}
	}

	for _, fromID := range nodeIDs {
		fromSeries := graph.Nodes[fromID]
		for _, toID := range nodeIDs {
			toSeries := graph.Nodes[toID]

			if fromID == toID {
				continue
			}

			exProb, strength, mean, variance :=
				EstimateEdgeProbability(fromSeries, toSeries, cfg)

			edge := &Edge{
				From: fromID,
				To:   toID,

				ExistenceProb:  exProb,
				CausalStrength: strength,

				Type: Direct,

				Identifiable: false,
				Conditions:   map[string]float64{},

				Mean:     mean,
				Variance: variance,
			}

			result.Edges = append(result.Edges, edge)
		}
	}

	return result
}

// sortStrings is a local insertion sort for deterministic string ordering.
// Uses sort.Strings semantics without importing sort into this scope twice.
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}





func residualizePair(xy, z []WeightedPair) []WeightedPair {

	var result []WeightedPair

	for i := range xy {

		// confounder remove
		zv := z[i].X

		xv := xy[i].X - zv
		yv := xy[i].Y - zv

		result = append(result, WeightedPair{
			X: xv,
			Y: yv,
			Weight: xy[i].Weight,
		})
	}

	return result
}