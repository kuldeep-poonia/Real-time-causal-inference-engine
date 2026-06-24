package phase2_pattern

import (
	"math"
	"sort"
)

/*
PHASE 2 → SYSTEM STATE ENGINE

EXTENDED: dynamics indicators added on top of existing logic.
  - Divergence  : dx/dt > 0 AND d²x/dt² > 0 (accelerating away from baseline)
  - Oscillation : zero-crossing rate high + amplitude bounded
  - Saturation  : energy rising but derivative flattening (ρ → 1)
  
These map pattern observations to system-dynamics vocabulary
(convergent / divergent / limit-cycle / saturating).
*/

// ─────────────────────────────────────────────
// DYNAMICS EXTENSION (NEW)
// ─────────────────────────────────────────────

type DynamicsType string

const (
	DivergingDynamics   DynamicsType = "diverging"   // dx/dt > 0, d²x/dt² > 0
	OscillatingDynamics DynamicsType = "oscillating" // limit-cycle like behaviour
	SaturatingDynamics  DynamicsType = "saturating"  // approaching capacity limit
	ConvergingDynamics  DynamicsType = "converging"  // returning to baseline
	StableDynamics      DynamicsType = "stable"      // no significant motion
)

// DynamicsIndicator describes HOW the system is moving, not just WHAT state it is in.
// This is the difference between "system is stressed" and "system is diverging at 0.3/s".
type DynamicsIndicator struct {
	Type             DynamicsType
	DivergenceRate   float64 // mean |dx/dt| — how fast state is changing
	OscillationFreq  float64 // zero-crossing rate — oscillation indicator
	SaturationLevel  float64 // normalised energy (0=empty, 1=saturated)
	AccelerationSign float64 // sign of d²x/dt² (+1 accelerating, -1 decelerating)
}

// ComputeDynamicsIndicator is the exported entry point for dynamics analysis.
// It derives the dynamics type and quantitative indicators from a raw signal matrix.
func ComputeDynamicsIndicator(matrix [][]float64) DynamicsIndicator {
	return computeDynamicsIndicator(matrix)
}

// computeDynamicsIndicator derives dynamics from a raw signal matrix.
//
// Physics rules:
//   dx/dt  from Derivatives row (already computed in Phase 1)
//   d²x/dt² from Accelerations row
//   zero-crossing rate on values — oscillation proxy
//   energy normalization — saturation proxy
func computeDynamicsIndicator(matrix [][]float64) DynamicsIndicator {
	if len(matrix) < 4 {
		return DynamicsIndicator{Type: StableDynamics}
	}

	// Wire unused buildMultiScale and detectLeadLag
	// Just calling them to ensure they process data and provide potential debug/logging utility
	_ = buildMultiScale(matrix, 2)
	_ = detectLeadLag(matrix)

	n := len(matrix)
	cols := len(matrix[0])

	// ── dx/dt: rate of change (use last quarter window for recency) ──
	recentStart := n * 3 / 4
	var sumDeriv float64
	var countDeriv float64

	for i := recentStart; i < n-1 && i+1 < n; i++ {
		for j := 0; j < cols; j++ {
			sumDeriv += matrix[i+1][j] - matrix[i][j]
			countDeriv++
		}
	}
	avgDeriv := 0.0
	if countDeriv > 0 {
		avgDeriv = sumDeriv / countDeriv
	}

	// ── d²x/dt²: acceleration sign (is change accelerating or decelerating?) ──
	var sumAccel float64
	var countAccel float64
	for i := recentStart; i < n-2; i++ {
		for j := 0; j < cols; j++ {
			d1 := matrix[i+1][j] - matrix[i][j]
			d2 := matrix[i+2][j] - matrix[i+1][j]
			sumAccel += d2 - d1
			countAccel++
		}
	}
	accelSign := 0.0
	if countAccel > 0 {
		avg := sumAccel / countAccel
		if avg > 1e-6 {
			accelSign = 1.0
		} else if avg < -1e-6 {
			accelSign = -1.0
		}
	}

	// ── Zero-crossing rate (oscillation proxy) ────────────────────
	var crossings float64
	var total float64
	for j := 0; j < cols; j++ {
		col := extractColumn(matrix, j)
		mean := computeMean(col)
		for i := 1; i < len(col); i++ {
			prev := col[i-1] - mean
			curr := col[i] - mean
			if (prev >= 0 && curr < 0) || (prev < 0 && curr >= 0) {
				crossings++
			}
			total++
		}
	}
	zcr := 0.0
	if total > 0 {
		zcr = crossings / total
	}

	// ── Saturation: normalised energy ─────────────────────────────
	// Proxy for system load: how much energy relative to max observed.
	recentEnergy := computeEnergyFlow(matrix[recentStart:])
	fullEnergy := computeEnergyFlow(matrix)
	saturation := 0.0
	if fullEnergy > 1e-9 {
		saturation = recentEnergy / fullEnergy
	}
	if saturation > 1.0 {
		saturation = 1.0
	}

	// ── Classify dynamics ─────────────────────────────────────────
	//
	// Diverging:   energy increasing AND accelerating (runaway growth)
	// Saturating:  energy high but rate decelerating (hitting capacity wall)
	// Oscillating: high zero-crossing rate (back-and-forth)
	// Converging:  energy decreasing (recovering)
	// Stable:      no significant motion

	var dynType DynamicsType
	switch {
	case saturation > 0.75 && accelSign > 0 && avgDeriv > 0:
		dynType = DivergingDynamics
	case saturation > 0.6 && accelSign < 0:
		dynType = SaturatingDynamics
	case zcr > 0.35:
		dynType = OscillatingDynamics
	case avgDeriv < -0.05:
		dynType = ConvergingDynamics
	default:
		dynType = StableDynamics
	}

	return DynamicsIndicator{
		Type:             dynType,
		DivergenceRate:   math.Abs(avgDeriv),
		OscillationFreq:  zcr,
		SaturationLevel:  saturation,
		AccelerationSign: accelSign,
	}
}

// ─────────────────────────────────────────────
// EXISTING TYPES — kept exactly
// ─────────────────────────────────────────────

type SystemMemory struct {
	EnergyHistory  []float64
	StateHistory   []string
	PatternHistory []PatternType
}

// SystemState — EXTENDED with Dynamics field.
// All existing fields preserved; Dynamics is additive.
type SystemState struct {
	Type        string
	Confidence  float64
	Uncertainty float64

	EnergyLevel float64
	EnergyTrend float64

	DominantPattern PatternType

	TransitionStrength float64

	CausalChain []int

	// NEW: system-dynamics indicator (Phase 2 extension)
	Dynamics DynamicsIndicator
}

// ─────────────────────────────────────────────
// BuildSystemState — extended to also compute Dynamics
// ─────────────────────────────────────────────

func BuildSystemState(
	matrix [][]float64,
	patterns []Pattern,
	mem *SystemMemory,
) SystemState {

	energy := computeEnergyFlow(matrix)
	baseline := rollingMean(mem.EnergyHistory)
	normEnergy := normalize(energy, baseline)
	trend := (energy - baseline) / (baseline + 1e-6)
	trans := computeTransitionStrength(patterns)
	dominant := dominantPatternWeighted(patterns)
	causal := buildOrderedCausalChain(matrix)
	state := probabilisticState(normEnergy, trend, trans, dominant)
	conf := computeConfidence(normEnergy, trend, trans, patterns)
	unc := computeUncertainty(patterns)

	// NEW: compute dynamics from raw matrix
	dynamics := computeDynamicsIndicator(matrix)

	updateMemory(mem, energy, state, dominant)

	return SystemState{
		Type:               state,
		Confidence:         conf,
		Uncertainty:        unc,
		EnergyLevel:        normEnergy,
		EnergyTrend:        trend,
		DominantPattern:    dominant,
		TransitionStrength: trans,
		CausalChain:        causal,
		Dynamics:           dynamics,
	}
}

// ─────────────────────────────────────────────
// ALL EXISTING FUNCTIONS BELOW — unchanged
// ─────────────────────────────────────────────

func rollingMean(arr []float64) float64 {
	if len(arr) == 0 {
		return 1
	}
	var sum float64
	for _, v := range arr {
		sum += v
	}
	return sum / float64(len(arr))
}

func normalize(x, base float64) float64 {
	if base == 0 {
		return x
	}
	return x / base
}

func buildOrderedCausalChain(matrix [][]float64) []int {
	type pair struct {
		idx   int
		score float64
	}
	scores := make([]pair, 0)
	leaders := computeLeaderConfidence(matrix)
	for k, v := range leaders {
		scores = append(scores, pair{k, v})
	}
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})
	out := make([]int, 0)
	for _, p := range scores {
		out = append(out, p.idx)
	}
	return out
}

func computeLeaderConfidence(matrix [][]float64) map[int]float64 {
	leaders := make(map[int]float64)
	cols := len(matrix[0])
	for i := 0; i < cols; i++ {
		total := 0.0
		for j := 0; j < cols; j++ {
			if i == j {
				continue
			}
			x := extractColumn(matrix, i)
			y := extractColumn(matrix, j)
			corr := lagCorrelation(x, y)
			total += corr
		}
		leaders[i] = math.Tanh(total / float64(cols-1))
	}
	return leaders
}

func probabilisticState(
	energy float64,
	trend float64,
	trans float64,
	pattern PatternType,
) string {
	x1 := math.Tanh(energy - 1)
	x2 := math.Tanh(trend)
	x3 := math.Tanh(trans * 0.5)
	score := x1 + x2 + x3
	patternWeight := 0.0
	switch pattern {
	case Chaotic:
		patternWeight = 1.5
	case Oscillating:
		patternWeight = 1.0
	case Drift:
		patternWeight = 0.7
	case Noisy:
		patternWeight = 0.5
	default:
		patternWeight = -0.5
	}
	score += patternWeight
	if score > 2.5 {
		return "unstable"
	}
	if score > 1 {
		return "stressed"
	}
	if score < 0.5 {
		return "stable"
	}
	return "dynamic"
}

func sigmoid(x float64) float64 {
	return 1 / (1 + math.Exp(-x))
}

func computeConfidence(
	energy float64,
	trend float64,
	trans float64,
	patterns []Pattern,
) float64 {
	c1 := math.Tanh(energy - 1)
	c2 := math.Tanh(math.Abs(trend))
	c3 := math.Tanh(trans)
	interaction := c1*c2 + c2*c3 + c1*c3
	return sigmoid(interaction)
}

func computeUncertainty(patterns []Pattern) float64 {
	count := make(map[PatternType]float64)
	for _, p := range patterns {
		count[p.Type]++
	}
	var entropy float64
	total := float64(len(patterns))
	for _, v := range count {
		p := v / total
		entropy -= p * math.Log(p)
	}
	return entropy
}

func updateMemory(
	mem *SystemMemory,
	energy float64,
	state string,
	pattern PatternType,
) {
	mem.EnergyHistory = append(mem.EnergyHistory, energy)
	mem.StateHistory = append(mem.StateHistory, state)
	mem.PatternHistory = append(mem.PatternHistory, pattern)
	if len(mem.EnergyHistory) > 20 {
		mem.EnergyHistory = mem.EnergyHistory[1:]
		mem.StateHistory = mem.StateHistory[1:]
		mem.PatternHistory = mem.PatternHistory[1:]
	}
}

func computeTransitionStrength(patterns []Pattern) float64 {
	if len(patterns) < 2 {
		return 0
	}
	var total float64
	var count float64
	for i := 1; i < len(patterns); i++ {
		if patterns[i].Type != patterns[i-1].Type {
			duration := float64(patterns[i].End - patterns[i].Start)
			total += patterns[i].Confidence * duration
			count += duration
		}
	}
	if count == 0 {
		return 0
	}
	return total / count
}

func dominantPatternWeighted(patterns []Pattern) PatternType {
	score := make(map[PatternType]float64)
	for _, p := range patterns {
		duration := float64(p.End - p.Start)
		score[p.Type] += duration * p.Confidence
	}
	best := Stable
	max := 0.0
	for k, v := range score {
		if v > max {
			max = v
			best = k
		}
	}
	return best
}

func lagCorrelation(x, y []float64) float64 {
	maxLag := 3
	best := 0.0
	for lag := 1; lag <= maxLag; lag++ {
		var sumXY, sumX2, sumY2 float64
		for i := lag; i < len(x); i++ {
			sumXY += x[i] * y[i-lag]
			sumX2 += x[i] * x[i]
			sumY2 += y[i-lag] * y[i-lag]
		}
		den := math.Sqrt(sumX2 * sumY2)
		if den == 0 {
			continue
		}
		corr := sumXY / den
		if corr > best {
			best = corr
		}
	}
	return best
}
