
package phase2_pattern

import (
	"math"
)



type ChangePoint struct {
	Index     int
	Score     float64
	Direction string // up / down / unstable
}

type Regime struct {
	Start   int
	End     int
	Quality string // stable / unstable / noisy
}

/*
ENTRY
*/
func DetectRegimes(data []float64) []Regime {

	if len(data) < 10 {
		return []Regime{{Start: 0, End: len(data), Quality: "stable"}}
	}

	window := adaptiveWindowSize(data)

	points := detectChangePoints(data, window)

	points = applyPenalty(points)

	regimes := buildRegimes(data, points)

	return regimes
}

/*
ADAPTIVE WINDOW

Window size scales with signal length and variability.
*/
func adaptiveWindowSize(data []float64) int {

	n := len(data)

	if n < 50 {
		return 3
	} else if n < 200 {
		return 5
	}

	return 10
}

/*
CORE DETECTION
*/
func detectChangePoints(data []float64, window int) []ChangePoint {

	points := make([]ChangePoint, 0)

	scores := make([]float64, len(data))

	// compute scores first (for normalization)
	for i := window; i < len(data)-window; i++ {

		left := data[i-window : i]
		right := data[i : i+window]

		score := computeNormalizedScore(left, right)

		scores[i] = score
	}

	threshold := adaptiveThreshold(scores)

	for i := window; i < len(data)-window; i++ {

		score := scores[i]

		if score > threshold {

			dir := detectDirection(data, i)

			points = append(points, ChangePoint{
				Index:     i,
				Score:     score,
				Direction: dir,
			})
		}
	}

	return points
}

/*
NORMALIZED SCORE

har component comparable
*/
func computeNormalizedScore(left, right []float64) float64 {

	meanL := computeMean(left)
	meanR := computeMean(right)

	varL := computeVariance(left, meanL)
	varR := computeVariance(right, meanR)

	changeL := computeChangeIntensity(left)
	changeR := computeChangeIntensity(right)

	// normalize components
	meanDiff := safeDiv(math.Abs(meanR-meanL), math.Abs(meanL)+1e-6)
	varDiff := safeDiv(math.Abs(varR-varL), varL+1e-6)
	changeDiff := safeDiv(math.Abs(changeR-changeL), changeL+1e-6)

	return meanDiff + varDiff + changeDiff
}

/*
ADAPTIVE THRESHOLD

distribution based
*/
func adaptiveThreshold(scores []float64) float64 {

	mean := computeMean(scores)
	std := math.Sqrt(computeVariance(scores, mean))

	// threshold = mean + k * std
	return mean + 1.5*std
}

/*
DIRECTION

increase / decrease / unstable
*/
func detectDirection(data []float64, idx int) string {

	if idx <= 0 || idx >= len(data)-1 {
		return "unknown"
	}

	diff := data[idx] - data[idx-1]

	if diff > 0 {
		return "up"
	} else if diff < 0 {
		return "down"
	}

	return "unstable"
}

/*
PENALTY — reduce noise

close points remove + score filter
*/
func applyPenalty(points []ChangePoint) []ChangePoint {

	if len(points) == 0 {
		return points
	}

	filtered := []ChangePoint{points[0]}

	for i := 1; i < len(points); i++ {

		last := filtered[len(filtered)-1]

		// minimum distance constraint
		if points[i].Index-last.Index < 5 {
			continue
		}

		filtered = append(filtered, points[i])
	}

	return filtered
}

/*
BUILD REGIMES + QUALITY
*/
func buildRegimes(data []float64, points []ChangePoint) []Regime {

	regimes := make([]Regime, 0)

	prev := 0

	for _, cp := range points {

		quality := evaluateRegime(data[prev:cp.Index])

		regimes = append(regimes, Regime{
			Start:   prev,
			End:     cp.Index,
			Quality: quality,
		})

		prev = cp.Index
	}

	// last regime
	regimes = append(regimes, Regime{
		Start:   prev,
		End:     len(data),
		Quality: evaluateRegime(data[prev:]),
	})

	return regimes
}

/*
REGIME QUALITY

basic physics interpretation
*/
func evaluateRegime(data []float64) string {

	if len(data) < 3 {
		return "stable"
	}

	variance := computeVariance(data, computeMean(data))
	change := computeChangeIntensity(data)

	if change > 2*variance {
		return "unstable"
	}

	if variance > 1 {
		return "noisy"
	}

	return "stable"
}





func safeDiv(a, b float64) float64 {
	if math.Abs(b) < 1e-9 {
		return 0
	}
	return a / b
}