
package phase2_pattern

import (
	"math"
)

/*
PHASE 2 → PATTERN INTELLIGENCE (FINAL LAYER)

yaha system "samajhta" hai

→ multi-scale hierarchy
→ energy dynamics
→ pattern transitions
→ causal hints

IMPORTANT:
ye layer Phase 2 ko complete karti hai
*/

/*
multi-scale hierarchy

recursive segmentation
*/
func buildMultiScale(segment [][]float64, depth int) []multiStats {

	if depth == 0 || len(segment) < 5 {
		return []multiStats{multiScaleStats(segment)}
	}

	mid := len(segment) / 2

	left := segment[:mid]
	right := segment[mid:]

	out := make([]multiStats, 0)

	out = append(out, multiScaleStats(segment))
	out = append(out, buildMultiScale(left, depth-1)...)
	out = append(out, buildMultiScale(right, depth-1)...)

	return out
}

/*
CORRELATION — max-based (not average)

strong coupling detect karega
*/
func maxCorr(corr [][]float64) float64 {

	maxVal := 0.0

	for i := 0; i < len(corr); i++ {
		for j := i + 1; j < len(corr); j++ {

			val := math.Abs(corr[i][j])

			if val > maxVal {
				maxVal = val
			}
		}
	}

	return maxVal
}

/*
ENERGY VIEW (physics-based)

system load / activity
*/
func computeEnergyFlow(segment [][]float64) float64 {

	total := 0.0

	for j := 0; j < len(segment[0]); j++ {
		col := extractColumn(segment, j)

		for _, v := range col {
			total += v * v
		}
	}

	return total / float64(len(segment))
}

/*
PATTERN TRANSITION MODEL

sequence-aware reasoning
*/
func analyzeTransitions(patterns []Pattern) []string {

	out := make([]string, len(patterns))

	for i := 1; i < len(patterns); i++ {

		prev := patterns[i-1].Type
		curr := patterns[i].Type

		out[i] = string(prev) + "→" + string(curr)
	}

	return out
}

/*
CAUSAL HINT — lead-lag signal

basic delay detection
*/
func detectLeadLag(segment [][]float64) map[int]int {

	leaders := make(map[int]int)

	cols := len(segment[0])

	for i := 0; i < cols; i++ {
		for j := 0; j < cols; j++ {

			if i == j {
				continue
			}

			lag := computeLag(extractColumn(segment, i), extractColumn(segment, j))

			if lag > 0 {
				leaders[i]++
			}
		}
	}

	return leaders
}

/*
simple lag detection (shifted correlation)
*/
func computeLag(x, y []float64) int {

	maxLag := 3
	bestLag := 0
	bestScore := 0.0

	for lag := 1; lag <= maxLag; lag++ {

		score := 0.0

		for i := lag; i < len(x); i++ {
			score += x[i] * y[i-lag]
		}

		if score > bestScore {
			bestScore = score
			bestLag = lag
		}
	}

	return bestLag
}

/*
CONFIDENCE REFINEMENT

avoid domination
*/
func refineConfidence(s multiStats, energy float64, corr float64) float64 {

	// normalize contributions
	c1 := safeDiv(s.changeLong, s.varLong+1e-6)
	c2 := math.Log(1 + s.entropy)
	c3 := safeDiv(energy, 1+s.changeLong)
	c4 := corr

	total := c1 + c2 + c3 + c4

	return 1 - math.Exp(-total)
}
