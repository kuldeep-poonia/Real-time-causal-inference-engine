package phase2_pattern

import (
	"math"
)

/*
PHASE 2 — FEATURE + STRUCTURE EXTRACTOR

Responsibilities:
  1. Preserve temporal structure via segmented analysis (early/mid/late windows).
  2. Detect sub-window behaviour (energy, momentum, volatility, trend).
  3. Capture cross-signal relationships via Pearson correlation matrix.

Returns a FeatureVector containing per-signal SegmentFeatures and a
CrossSignalFeature correlation matrix rather than a single scalar summary.
*/

type SegmentFeature struct {
	Mean            float64
	Variance        float64
	ChangeIntensity float64
	Acceleration    float64
	Entropy         float64
	ZeroCrossRate   float64

	Energy     float64
	Momentum   float64
	Volatility float64
	Trend      float64
}

type SignalFeatures struct {
	Segments []SegmentFeature
}

type CrossSignalFeature struct {
	CorrelationMatrix [][]float64
}

type FeatureVector struct {
	Signals []SignalFeatures
	Cross   CrossSignalFeature
}

/*
ENTRY

matrix = [time][signal]
*/
func ExtractFeatures(matrix [][]float64) FeatureVector {

	rows := len(matrix)
	if rows == 0 {
		return FeatureVector{}
	}

	cols := len(matrix[0])

	signals := make([]SignalFeatures, cols)

	// per-signal processing
	for j := 0; j < cols; j++ {
		column := extractColumn(matrix, j)
		signals[j] = processSignal(column)
	}

	// cross-signal relation
	corr := computeCorrelationMatrix(matrix)

	return FeatureVector{
		Signals: signals,
		Cross: CrossSignalFeature{
			CorrelationMatrix: corr,
		},
	}
}

/*
signal ko segments me todna
*/
func processSignal(data []float64) SignalFeatures {

	segments := splitIntoSegments(data, 3) // fixed 3 (early/mid/late)

	out := make([]SegmentFeature, len(segments))

	for i, seg := range segments {
		out[i] = computeSegmentFeature(seg)
	}

	return SignalFeatures{
		Segments: out,
	}
}

func splitIntoSegments(data []float64, parts int) [][]float64 {

	n := len(data)
	if n < parts {
		return [][]float64{data}
	}

	out := make([][]float64, 0, parts)

	// adaptive segmentation (log scale)
	base := float64(n) / float64(parts)

	start := 0

	for i := 0; i < parts; i++ {

		size := int(base * (1 + 0.3*math.Sin(float64(i)))) // slight variation

		end := start + size

		if i == parts-1 || end > n {
			end = n
		}

		if start >= n {
			break
		}

		out = append(out, data[start:end])
		start = end
	}

	return out
}

/*
segment level features
*/
func computeSegmentFeature(data []float64) SegmentFeature {

	mean := computeMean(data)
	variance := computeVariance(data, mean)
	change := computeChangeIntensity(data)
	acc := computeAcceleration(data)
	entropy := computeEntropy(data)
	zcr := computeZeroCrossRateCentered(data, mean)

	// ✅ PHYSICS ADDITION

	var energy float64
	var momentum float64
	var volatility float64

	for i := 0; i < len(data); i++ {
		v := data[i]

		energy += v * v

		if i > 0 {
			diff := data[i] - data[i-1]
			momentum += diff
			volatility += math.Abs(diff)
		}
	}



	// normalized energy (scale independent)
normEnergy := energy / float64(len(data)+1)




	// trend (simple slope)
	trend := 0.0
	if len(data) > 1 {
		trend = data[len(data)-1] - data[0]
	}

	return SegmentFeature{
	Mean:            mean,
	Variance:        variance,
	ChangeIntensity: change,
	Acceleration:    acc,
	Entropy:         entropy,
	ZeroCrossRate:   zcr,

	Energy:     normEnergy,
	Momentum:   momentum,
	Volatility: volatility,
	Trend:      trend,
}
}
/*
helpers
*/

func extractColumn(matrix [][]float64, col int) []float64 {
	out := make([]float64, len(matrix))
	for i := 0; i < len(matrix); i++ {
		out[i] = matrix[i][col]
	}
	return out
}

func computeMean(data []float64) float64 {
	var sum float64
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

func computeVariance(data []float64, mean float64) float64 {
	var sum float64
	for _, v := range data {
		diff := v - mean
		sum += diff * diff
	}
	return sum / float64(len(data))
}

/*
absolute change
*/
func computeChangeIntensity(data []float64) float64 {

	var sum float64

	for i := 1; i < len(data); i++ {
		sum += math.Abs(data[i] - data[i-1])
	}

	return sum / float64(len(data)-1)
}

/*
second derivative
*/
func computeAcceleration(data []float64) float64 {

	var sum float64
	count := 0

	for i := 2; i < len(data); i++ {
		acc := data[i] - 2*data[i-1] + data[i-2]
		sum += math.Abs(acc)
		count++
	}

	if count == 0 {
		return 0
	}

	return sum / float64(count)
}

/*
mean centered zero crossing
*/
func computeZeroCrossRateCentered(data []float64, mean float64) float64 {

	var count float64

	for i := 1; i < len(data); i++ {

		prev := data[i-1] - mean
		curr := data[i] - mean

		if (prev >= 0 && curr < 0) || (prev < 0 && curr >= 0) {
			count++
		}
	}

	return count / float64(len(data)-1)
}

/*
entropy (normalized)

scale independent banaya
*/
func computeEntropy(data []float64) float64 {

	var sum float64
	for _, v := range data {
		sum += math.Abs(v)
	}

	if sum == 0 {
		return 0
	}

	var entropy float64

	for _, v := range data {
		p := math.Abs(v) / sum
		if p > 0 {
			entropy -= p * math.Log(p)
		}
	}

	return entropy
}

/*
CROSS SIGNAL CORE

correlation matrix
*/
func computeCorrelationMatrix(matrix [][]float64) [][]float64 {

	cols := len(matrix[0])

	corr := make([][]float64, cols)

	for i := 0; i < cols; i++ {
		corr[i] = make([]float64, cols)

		for j := 0; j < cols; j++ {

			x := extractColumn(matrix, i)
			y := extractColumn(matrix, j)

			corr[i][j] = computeCorrelation(x, y)
		}
	}

	return corr
}

/*
pearson correlation

Handles zero-variance edge cases per standard statistical conventions:
  - Self-correlation (identical signals): return 1.0 (diagonal invariant)
  - Cross-correlation where one or both have zero variance: return 0.0
    (no linear relationship is detectable when variance is absent)
*/
func computeCorrelation(x, y []float64) float64 {

	if len(x) == 0 || len(x) != len(y) {
		return 0
	}

	meanX := computeMean(x)
	meanY := computeMean(y)

	var sumXY, sumX2, sumY2 float64

	for i := 0; i < len(x); i++ {
		dx := x[i] - meanX
		dy := y[i] - meanY

		sumXY += dx * dy
		sumX2 += dx * dx
		sumY2 += dy * dy
	}

	xZeroVar := sumX2 < 1e-30
	yZeroVar := sumY2 < 1e-30

	if xZeroVar || yZeroVar {
		// Both signals have zero variance: check if they are identical
		// (self-correlation diagonal case or identical constant columns).
		if xZeroVar && yZeroVar {
			// If every element is equal between x and y, this is
			// self-correlation (or effectively identical signals) → 1.0.
			identical := true
			for i := range x {
				if math.Abs(x[i]-y[i]) > 1e-15 {
					identical = false
					break
				}
			}
			if identical {
				return 1.0
			}
		}
		// One or both have zero variance but they differ → no correlation.
		return 0.0
	}

	den := math.Sqrt(sumX2 * sumY2)

	// Clamp to [-1, 1] to guard against floating-point overshoot.
	r := sumXY / den
	if r > 1.0 {
		r = 1.0
	} else if r < -1.0 {
		r = -1.0
	}
	return r
}



func computeLaggedCorrelation(x, y []float64, lag int) float64 {

	n := len(x)
	if n <= lag {
		return 0
	}

	var sum float64

	for i := lag; i < n; i++ {
		sum += x[i-lag] * y[i]
	}

	return sum / float64(n-lag)
}