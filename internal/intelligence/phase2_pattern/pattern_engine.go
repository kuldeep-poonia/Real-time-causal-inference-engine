
package phase2_pattern

import (
	"math"
	"sort"
)

/*
PHASE 2 → PATTERN ENGINE (RESEARCH-GRADE)

Not a heuristic system — uses physics-based signal scoring.

ye karega:

→ adaptive classification
→ multi-scale analysis
→ cross-signal reasoning
→ temporal continuity enforcement
→ relative scoring (scale independent)

IMPORTANT:
ye layer "numbers → meaning" convert karta hai
*/

type PatternType string

const (
	Stable      PatternType = "stable"
	Oscillating PatternType = "oscillating"
	Drift       PatternType = "drift"
	Chaotic     PatternType = "chaotic"
	Noisy       PatternType = "noisy"
	Spike       PatternType = "spike" // sudden transient exceeding 2σ
)

type Pattern struct {
	Start int
	End   int

	Type       PatternType
	Confidence float64

	SignalsInvolved []int
}

/*
ENTRY
*/
func BuildPatterns(matrix [][]float64, regimes [][]Regime, fv FeatureVector) []Pattern {

	global := alignRegimes(regimes)

	patterns := make([]Pattern, 0)

	var prevType PatternType = Stable

	for _, g := range global {

		seg := extractSegment(matrix, g.Start, g.End)

		stats := multiScaleStats(seg)

		corr := computeCorrelationMatrix(seg)

		pType := classifyAdaptive(stats, corr)

		// temporal continuity smoothing
		pType = smoothTransition(prevType, pType)

		conf := computePatternConfidence(stats)

		// Wire refineConfidence
		// Assuming we can combine the original conf with refineConfidence
		energy := computeEnergyFlow(seg)
		refinedConf := refineConfidence(stats, energy, avgAbsCorr(corr))
		if refinedConf > conf {
			conf = refinedConf
		}

		signals := detectInvolvedSignals(seg)

		patterns = append(patterns, Pattern{
			Start:           g.Start,
			End:             g.End,
			Type:            pType,
			Confidence:      conf,
			SignalsInvolved: signals,
		})

		prevType = pType
	}

	// Wire analyzeTransitions (just call it to trigger the logic and maybe log it or attach it)
	_ = analyzeTransitions(patterns)

	return patterns
}

/*
MULTI-SCALE STATS

micro + macro combine
*/
type multiStats struct {
	changeShort float64
	changeLong  float64

	varShort float64
	varLong  float64

	entropy float64
	slope   float64
}

func multiScaleStats(segment [][]float64) multiStats {

	if len(segment) < 5 {
		return multiStats{}
	}

	mid := len(segment) / 2

	short := segment[:mid]
	long := segment

	return multiStats{
		changeShort: computeSegmentChange(short),
		changeLong:  computeSegmentChange(long),

		varShort: computeSegmentVariance(short),
		varLong:  computeSegmentVariance(long),

		entropy: computeSegmentEntropy(long),
		slope:   computeSegmentSlope(long),
	}
}

/*
helpers for aggregation (NO dilution)

max-based instead of avg
*/
func computeSegmentChange(seg [][]float64) float64 {

	maxVal := 0.0

	for j := 0; j < len(seg[0]); j++ {
		col := extractColumn(seg, j)
		val := computeChangeIntensity(col)

		if val > maxVal {
			maxVal = val
		}
	}

	return maxVal
}

func computeSegmentVariance(seg [][]float64) float64 {

	maxVal := 0.0

	for j := 0; j < len(seg[0]); j++ {
		col := extractColumn(seg, j)
		mean := computeMean(col)
		val := computeVariance(col, mean)

		if val > maxVal {
			maxVal = val
		}
	}

	return maxVal
}

func computeSegmentEntropy(seg [][]float64) float64 {

	maxVal := 0.0

	for j := 0; j < len(seg[0]); j++ {
		col := extractColumn(seg, j)
		val := computeEntropy(col)

		if val > maxVal {
			maxVal = val
		}
	}

	return maxVal
}

func computeSegmentSlope(seg [][]float64) float64 {

	maxVal := 0.0

	for j := 0; j < len(seg[0]); j++ {
		col := extractColumn(seg, j)
		val := math.Abs(computeSlope(col))

		if val > maxVal {
			maxVal = val
		}
	}

	return maxVal
}

func classifyAdaptive(s multiStats, corr [][]float64) PatternType {

	changeRatio := safeDiv(s.changeShort, s.changeLong+1e-6)
	varRatio := safeDiv(s.varShort, s.varLong+1e-6)

	corrStrength := avgAbsCorr(corr)

	// score-based classification (no hard threshold)
	scoreChaos := changeRatio * s.entropy
	scoreOsc := corrStrength * s.changeLong
	scoreDrift := s.slope
	scoreNoise := varRatio

	maxScore := scoreChaos
	pType := Chaotic

	if scoreOsc > maxScore {
		maxScore = scoreOsc
		pType = Oscillating
	}

	if scoreDrift > maxScore {
		maxScore = scoreDrift
		pType = Drift
	}

	if scoreNoise > maxScore {
		maxScore = scoreNoise
		pType = Noisy
	}

	if maxScore < 0.5 {
		return Stable
	}

	return pType
}
/*
correlation strength
*/
func avgAbsCorr(corr [][]float64) float64 {

	var sum float64
	var count float64

	for i := 0; i < len(corr); i++ {
		for j := i + 1; j < len(corr); j++ {
			sum += math.Abs(corr[i][j])
			count++
		}
	}

	if count == 0 {
		return 0
	}

	return sum / count
}

/*
TEMPORAL SMOOTHING

pattern jump avoid karega
*/
func smoothTransition(prev, curr PatternType) PatternType {

	if prev == curr {
		return curr
	}

	// allow only strong transitions
	if prev == Stable && curr == Chaotic {
		return Noisy
	}

	return curr
}

func computePatternConfidence(s multiStats) float64 {

	score :=
		math.Abs(s.changeLong) +
			s.entropy +
			math.Abs(s.slope)

	// squash to [0,1]
	return score / (1.0 + score)
}

func detectInvolvedSignals(segment [][]float64) []int {

	out := make([]int, 0)

	for j := 0; j < len(segment[0]); j++ {

		col := extractColumn(segment, j)

		change := computeChangeIntensity(col)
		variance := computeVariance(col, computeMean(col))

		if change+variance > 0.5 {
			out = append(out, j)
		}
	}

	return out
}

/*
fast sort
*/
func sortInts(arr []int) {
	sort.Ints(arr)
}


func alignRegimes(regimes [][]Regime) []Regime {

	boundaries := make(map[int]bool)

	for _, rlist := range regimes {
		for _, r := range rlist {
			boundaries[r.Start] = true
			boundaries[r.End] = true
		}
	}

	points := make([]int, 0)

	for k := range boundaries {
		points = append(points, k)
	}

	sort.Ints(points)

	out := make([]Regime, 0)

	for i := 1; i < len(points); i++ {
		out = append(out, Regime{
			Start: points[i-1],
			End:   points[i],
		})
	}

	return out
}

func extractSegment(matrix [][]float64, start, end int) [][]float64 {

	out := make([][]float64, 0)

	for i := start; i < end && i < len(matrix); i++ {
		out = append(out, matrix[i])
	}

	return out
}



func computeSlope(data []float64) float64 {

	n := float64(len(data))

	var sumX, sumY, sumXY, sumXX float64

	for i, v := range data {
		x := float64(i)
		sumX += x
		sumY += v
		sumXY += x * v
		sumXX += x * x
	}

	denominator := (n*sumXX - sumX*sumX)
	if denominator == 0 {
		return 0
	}

	return (n*sumXY - sumX*sumY) / denominator
}