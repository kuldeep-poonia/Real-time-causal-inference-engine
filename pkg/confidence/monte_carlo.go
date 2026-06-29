package confidence

import (
	"math"
	"math/rand"
	"sort"
	"time"
)

// MCOutput represents the statistical results of the Monte Carlo simulation.
type MCOutput struct {
	Mean              float64
	Std               float64
	Median            float64
	P05               float64
	P95               float64
	Entropy           float64
	SampleCount       int
	AleatoricUncert   float64
	EpistemicUncert   float64
}

// MCEngine drives the Monte Carlo uncertainty quantification.
type MCEngine struct {
	rng *rand.Rand
}

// NewMCEngine creates a new Monte Carlo engine.
func NewMCEngine() *MCEngine {
	return &MCEngine{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// RunAdaptive runs Monte Carlo sampling until the 95% CI converges or maxSamples is reached.
// It uses a Multivariate Gaussian model to account for correlated components.
// means: [Bayesian, Causal, Physics, Telemetry]
// cov: 4x4 covariance matrix (already Cholesky decomposed into lower triangular L)
func (e *MCEngine) RunAdaptive(means []float64, L [][]float64, weights []float64, maxSamples int, tolerance float64) MCOutput {
	var samples []float64
	var sum, sumSq float64

	dim := len(means)

	// We distinguish Aleatoric (data noise simulated here) vs Epistemic (model uncertainty)
	// We'll estimate epistemic based on the variance of the weights or the prior variance.
	var prevP05, prevP95 float64

	for i := 0; i < maxSamples; i++ {
		// 1. Generate independent standard normals
		z := make([]float64, dim)
		for j := 0; j < dim; j++ {
			z[j] = e.rng.NormFloat64()
		}

		// 2. Multiply by Cholesky factor L and add mean
		x := make([]float64, dim)
		for j := 0; j < dim; j++ {
			x[j] = means[j]
			for k := 0; k <= j; k++ {
				x[j] += L[j][k] * z[k]
			}
			// Bound between 0 and 1
			if x[j] < 0 {
				x[j] = 0
			}
			if x[j] > 1 {
				x[j] = 1
			}
		}

		// 3. Compute score
		score := 0.0
		for j := 0; j < dim; j++ {
			score += weights[j] * x[j]
		}
		
		if score < 0 { score = 0 }
		if score > 1 { score = 1 }

		samples = append(samples, score)
		sum += score
		sumSq += score * score

		// Check for convergence every 100 samples
		if i > 0 && i%100 == 0 {
			// Sort a copy of samples to find percentiles
			sorted := make([]float64, len(samples))
			copy(sorted, samples)
			sort.Float64s(sorted)
			
			p05Idx := int(0.05 * float64(len(sorted)))
			p95Idx := int(0.95 * float64(len(sorted)))
			
			currentP05 := sorted[p05Idx]
			currentP95 := sorted[p95Idx]

			if i > 100 {
				diffP05 := math.Abs(currentP05 - prevP05)
				diffP95 := math.Abs(currentP95 - prevP95)
				
				if diffP05 < tolerance && diffP95 < tolerance {
					break // Converged
				}
			}
			
			prevP05 = currentP05
			prevP95 = currentP95
		}
	}

	// Final Statistics Calculation
	sort.Float64s(samples)
	n := float64(len(samples))
	mean := sum / n
	std := math.Sqrt((sumSq / n) - (mean * mean))
	
	median := samples[int(0.50*n)]
	p05 := samples[int(0.05*n)]
	p95 := samples[int(0.95*n)]

	// Simple entropy estimation of the continuous distribution assuming normality
	// H(x) = 0.5 * ln(2 * pi * e * sigma^2)
	entropy := 0.0
	if std > 0 {
		entropy = 0.5 * math.Log(2*math.Pi*math.E*std*std)
	}

	// Aleatoric vs Epistemic approximation
	// We'll define Aleatoric as the intrinsic variance of the sampling (std^2)
	// Epistemic could be defined by the lack of data (1 - mean of telemetry metric)
	aleatoric := std * std
	epistemic := (1.0 - means[3]) * 0.5 // Telemetry quality impacts epistemic

	return MCOutput{
		Mean:            mean,
		Std:             std,
		Median:          median,
		P05:             p05,
		P95:             p95,
		Entropy:         entropy,
		SampleCount:     len(samples),
		AleatoricUncert: aleatoric,
		EpistemicUncert: epistemic,
	}
}

// BuildCholesky is a helper that returns a hardcoded lower triangular matrix L
// for a predefined covariance matrix of the 4 components. 
// In a real system, this could be learned. Here we assume positive correlation
// between Bayesian (0) and Causal (1), and Physics (2) and Telemetry (3).
func BuildCholesky(baseVariance float64) [][]float64 {
	// Covariance matrix roughly:
	// [ 1.0  0.5  0.2  0.1 ]
	// [ 0.5  1.0  0.3  0.1 ]
	// [ 0.2  0.3  1.0  0.4 ]
	// [ 0.1  0.1  0.4  1.0 ] * baseVariance
	
	// Pre-computed Cholesky decomposition L (such that L * L^T = Cov)
	L := [][]float64{
		{1.0, 0.0, 0.0, 0.0},
		{0.5, 0.866, 0.0, 0.0},
		{0.2, 0.231, 0.952, 0.0},
		{0.1, 0.057, 0.385, 0.916},
	}
	
	// Scale by standard deviation
	stdDev := math.Sqrt(baseVariance)
	for i := range L {
		for j := range L[i] {
			L[i][j] *= stdDev
		}
	}
	
	return L
}
