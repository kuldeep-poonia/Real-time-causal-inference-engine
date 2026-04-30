package data

import (
	"math"
	"math/rand"
)

/*
REALISTIC DATA GENERATION

This replaces synthetic shortcuts with realistic data simulation that includes:
- Multi-node causal chains
- Noise and partial observability
- Missing values
- Irregular timestamps
*/

type DataPoint struct {
	Timestamp float64
	Values    map[string]float64
	Missing   map[string]bool // Track missing values
}

type Dataset struct {
	Points []DataPoint
	Nodes  []string
}

/*
GenerateRealisticCausalData creates multi-step causal chains with realistic noise.

Structure: A → B → C (chain causality)
- A: root cause (arrival rate driven)
- B: intermediate (depends on A with lag)
- C: observed effect (depends on B with lag)

Each timestep:

	A(t) = baseRate + noise
	B(t) = 0.7*A(t-1) + 0.3*B(t-1) + noise (lag-1 causality)
	C(t) = 0.8*B(t-1) + 0.2*C(t-1) + noise (lag-1 causality)
*/
// GenerateRealisticCausalData creates multi-step causal chains with realistic noise.
// Uses the global default seed (42) for reproducibility in production.
// Use GenerateRealisticCausalDataWithSeed for explicit seed control in tests.
func GenerateRealisticCausalData(
	baseRate float64,
	timeSteps int,
	noiseLevel float64,
	missingFraction float64,
) *Dataset {
	return GenerateRealisticCausalDataWithSeed(baseRate, timeSteps, noiseLevel, missingFraction, 42)
}

// GenerateRealisticCausalDataWithSeed is the fully deterministic variant.
func GenerateRealisticCausalDataWithSeed(
	baseRate float64,
	timeSteps int,
	noiseLevel float64,
	missingFraction float64,
	seed int64,
) *Dataset {
	rng := rand.New(rand.NewSource(seed))

	dataset := &Dataset{
		Points: make([]DataPoint, 0),
		Nodes:  []string{"A", "B", "C"},
	}

	// Initialize series
	a_vals := make([]float64, timeSteps)
	b_vals := make([]float64, timeSteps)
	c_vals := make([]float64, timeSteps)

	// Initial conditions
	a_vals[0] = baseRate + (rng.Float64()-0.5)*2*noiseLevel
	b_vals[0] = 0.5 * a_vals[0]
	c_vals[0] = 0.5 * b_vals[0]

	// Generate causal chain: A → B → C with noise
	for t := 1; t < timeSteps; t++ {
		// A: root cause, driven by base rate + noise
		a_vals[t] = baseRate + (rng.Float64()-0.5)*2*noiseLevel

		// B: depends on A(t-1) + its own inertia + noise
		// This creates lag-1 causal relationship: A(t-1) → B(t)
		b_vals[t] = 0.7*a_vals[t-1] + 0.3*b_vals[t-1] + (rng.Float64()-0.5)*2*noiseLevel
		if b_vals[t] < 0 {
			b_vals[t] = 0
		}

		// C: depends on B(t-1) + its own inertia + noise
		// This creates lag-1 causal relationship: B(t-1) → C(t)
		c_vals[t] = 0.8*b_vals[t-1] + 0.2*c_vals[t-1] + (rng.Float64()-0.5)*2*noiseLevel
		if c_vals[t] < 0 {
			c_vals[t] = 0
		}
	}

	// Create dataset with timestamps
	for t := 0; t < timeSteps; t++ {
		point := DataPoint{
			Timestamp: float64(t),
			Values:    make(map[string]float64),
			Missing:   make(map[string]bool),
		}

		// Add values
		point.Values["A"] = a_vals[t]
		point.Values["B"] = b_vals[t]
		point.Values["C"] = c_vals[t]

		// Randomly mark some values as missing
		for _, node := range []string{"A", "B", "C"} {
			if rng.Float64() < missingFraction {
				point.Missing[node] = true
				// Don't delete the value, just mark as missing
				// Phase 4 should handle partial observability
			}
		}

		dataset.Points = append(dataset.Points, point)
	}

	return dataset
}

/*
GenerateInterventionScenario creates data where intervention changes causality.

Before intervention: A → C (A overloaded, causes C to spike)
After intervention: A → C link disappears (intervention removes A's effect)
*/
func GenerateInterventionScenario(
	preInterventionSteps int,
	postInterventionSteps int,
	noiseLevel float64,
) (*Dataset, *Dataset) {

	// Pre-intervention: A → C with strong coupling
	pre := &Dataset{
		Points: make([]DataPoint, 0),
		Nodes:  []string{"A", "C"},
	}

	a_pre := make([]float64, preInterventionSteps)
	c_pre := make([]float64, preInterventionSteps)

	for t := 0; t < preInterventionSteps; t++ {
		a_pre[t] = 10.0 + (rand.Float64()-0.5)*2*noiseLevel // High load

		if t == 0 {
			c_pre[t] = a_pre[t]
		} else {
			// Strong causal coupling: A(t-1) strongly affects C(t)
			c_pre[t] = 0.9*a_pre[t-1] + 0.1*c_pre[t-1] + (rand.Float64()-0.5)*2*noiseLevel
		}

		point := DataPoint{
			Timestamp: float64(t),
			Values: map[string]float64{
				"A": a_pre[t],
				"C": c_pre[t],
			},
			Missing: make(map[string]bool),
		}
		pre.Points = append(pre.Points, point)
	}

	// Post-intervention: A reduced, C becomes independent
	post := &Dataset{
		Points: make([]DataPoint, 0),
		Nodes:  []string{"A", "C"},
	}

	a_post := make([]float64, postInterventionSteps)
	c_post := make([]float64, postInterventionSteps)

	for t := 0; t < postInterventionSteps; t++ {
		a_post[t] = 2.0 + (rand.Float64()-0.5)*2*noiseLevel // Low load (intervened)

		if t == 0 {
			c_post[t] = c_pre[len(c_pre)-1] * 0.5 // Initial condition after intervention
		} else {
			// Weak coupling after intervention (A doesn't drive C anymore)
			c_post[t] = 0.1*a_post[t-1] + 0.9*c_post[t-1] + (rand.Float64()-0.5)*2*noiseLevel
		}

		point := DataPoint{
			Timestamp: float64(preInterventionSteps + t),
			Values: map[string]float64{
				"A": a_post[t],
				"C": c_post[t],
			},
			Missing: make(map[string]bool),
		}
		post.Points = append(post.Points, point)
	}

	return pre, post
}

/*
EstimateCorrelation computes correlation between two series.
Used for validation and comparison with Phase 4's causal effects.
*/
func EstimateCorrelation(series1, series2 []float64) float64 {
	if len(series1) != len(series2) || len(series1) == 0 {
		return 0
	}

	mean1, mean2 := 0.0, 0.0
	for i := 0; i < len(series1); i++ {
		mean1 += series1[i]
		mean2 += series2[i]
	}
	mean1 /= float64(len(series1))
	mean2 /= float64(len(series2))

	cov, var1, var2 := 0.0, 0.0, 0.0
	for i := 0; i < len(series1); i++ {
		dev1 := series1[i] - mean1
		dev2 := series2[i] - mean2
		cov += dev1 * dev2
		var1 += dev1 * dev1
		var2 += dev2 * dev2
	}

	if var1 == 0 || var2 == 0 {
		return 0
	}

	return cov / math.Sqrt(var1*var2)
}

/*
ExtractTimeSeries extracts a single variable's time series from dataset.
Fills missing values with last observation carried forward (LOCF).
*/
func ExtractTimeSeries(dataset *Dataset, nodeName string) []float64 {
	series := make([]float64, 0)
	lastValue := 0.0
	hasValue := false

	for _, point := range dataset.Points {
		if !point.Missing[nodeName] {
			series = append(series, point.Values[nodeName])
			lastValue = point.Values[nodeName]
			hasValue = true
		} else if hasValue {
			// Fill missing with LOCF (last observation carried forward)
			series = append(series, lastValue)
		} else {
			// No previous value, use 0
			series = append(series, 0.0)
		}
	}
	return series
}

/* ===========================
   COMPLEX CAUSAL STRESS TESTS
=========================== */

/*
GenerateConfounderData: Z → A, Z → C, A → C
Both Z and A appear to cause C, but only A is direct cause.
Z is confounder that must be blocked.
*/
func GenerateConfounderData(timeSteps int, noiseLevel float64) *Dataset {
	dataset := &Dataset{
		Points: make([]DataPoint, 0),
		Nodes:  []string{"Z", "A", "C"},
	}

	z_vals := make([]float64, timeSteps)
	a_vals := make([]float64, timeSteps)
	c_vals := make([]float64, timeSteps)

	z_vals[0] = 5.0 + (rand.Float64()-0.5)*2*noiseLevel
	a_vals[0] = 0.6*z_vals[0] + (rand.Float64()-0.5)*2*noiseLevel
	c_vals[0] = 0.7*a_vals[0] + (rand.Float64()-0.5)*2*noiseLevel

	for t := 1; t < timeSteps; t++ {
		// Z: independent root confounder
		z_vals[t] = 5.0 + (rand.Float64()-0.5)*2*noiseLevel

		// A: strongly affected by Z (confounded)
		a_vals[t] = 0.6*z_vals[t-1] + 0.3*a_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel

		// C: directly caused by A, also indirectly influenced by Z through A
		// This creates backdoor path: C ← A ← Z
		c_vals[t] = 0.7*a_vals[t-1] + 0.2*c_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel
	}

	for t := 0; t < timeSteps; t++ {
		dataset.Points = append(dataset.Points, DataPoint{
			Timestamp: float64(t),
			Values: map[string]float64{
				"Z": z_vals[t],
				"A": a_vals[t],
				"C": c_vals[t],
			},
			Missing: make(map[string]bool),
		})
	}
	return dataset
}

/*
GenerateSpuriousData: Z → A, Z → C (no direct A → C)
A and C are only correlated through Z.
Phase 3 may falsely detect A → C due to correlation.
Phase 4 must reject A as direct cause when conditioned on Z.
*/
func GenerateSpuriousData(timeSteps int, noiseLevel float64) *Dataset {
	dataset := &Dataset{
		Points: make([]DataPoint, 0),
		Nodes:  []string{"Z", "A", "C"},
	}

	z_vals := make([]float64, timeSteps)
	a_vals := make([]float64, timeSteps)
	c_vals := make([]float64, timeSteps)

	z_vals[0] = 5.0 + (rand.Float64()-0.5)*2*noiseLevel
	a_vals[0] = 0.8*z_vals[0] + (rand.Float64()-0.5)*2*noiseLevel
	c_vals[0] = 0.8*z_vals[0] + (rand.Float64()-0.5)*2*noiseLevel

	for t := 1; t < timeSteps; t++ {
		// Z: independent root cause
		z_vals[t] = 5.0 + (rand.Float64()-0.5)*2*noiseLevel

		// A: caused by Z (indirect path)
		a_vals[t] = 0.8*z_vals[t-1] + 0.2*a_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel

		// C: also caused by Z, but NOT by A (no direct path A→C)
		// Despite correlation between A and C, the causal path is Z→C only
		c_vals[t] = 0.8*z_vals[t-1] + 0.2*c_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel
	}

	for t := 0; t < timeSteps; t++ {
		dataset.Points = append(dataset.Points, DataPoint{
			Timestamp: float64(t),
			Values: map[string]float64{
				"Z": z_vals[t],
				"A": a_vals[t],
				"C": c_vals[t],
			},
			Missing: make(map[string]bool),
		})
	}
	return dataset
}

/*
GenerateColliderData: A → C ← B
A and B are independent root causes that meet at C.
Opening the collider (conditioning on C) would create false A-B correlation.
Phase 4 should NOT infer A ↔ B even if data shows correlation in certain samples.
*/
func GenerateColliderData(timeSteps int, noiseLevel float64) *Dataset {
	dataset := &Dataset{
		Points: make([]DataPoint, 0),
		Nodes:  []string{"A", "B", "C"},
	}

	a_vals := make([]float64, timeSteps)
	b_vals := make([]float64, timeSteps)
	c_vals := make([]float64, timeSteps)

	a_vals[0] = 3.0 + (rand.Float64()-0.5)*2*noiseLevel
	b_vals[0] = 4.0 + (rand.Float64()-0.5)*2*noiseLevel
	c_vals[0] = 0.5*a_vals[0] + 0.5*b_vals[0]

	for t := 1; t < timeSteps; t++ {
		// A and B: independent root causes
		a_vals[t] = 3.0 + (rand.Float64()-0.5)*2*noiseLevel
		b_vals[t] = 4.0 + (rand.Float64()-0.5)*2*noiseLevel

		// C: collider - affected by both A and B independently
		// No feedback from C to A or B
		c_vals[t] = 0.5*a_vals[t-1] + 0.5*b_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel
	}

	for t := 0; t < timeSteps; t++ {
		dataset.Points = append(dataset.Points, DataPoint{
			Timestamp: float64(t),
			Values: map[string]float64{
				"A": a_vals[t],
				"B": b_vals[t],
				"C": c_vals[t],
			},
			Missing: make(map[string]bool),
		})
	}
	return dataset
}

/*
GenerateMediatorData: A → B → C
A is root cause, B is intermediate (mediator), C is final effect.
Phase 4 should rank: A (primary), B (secondary via mediation).
*/
func GenerateMediatorData(timeSteps int, noiseLevel float64) *Dataset {
	dataset := &Dataset{
		Points: make([]DataPoint, 0),
		Nodes:  []string{"A", "B", "C"},
	}

	a_vals := make([]float64, timeSteps)
	b_vals := make([]float64, timeSteps)
	c_vals := make([]float64, timeSteps)

	a_vals[0] = 5.0 + (rand.Float64()-0.5)*2*noiseLevel
	b_vals[0] = 0.8 * a_vals[0]
	c_vals[0] = 0.8 * b_vals[0]

	for t := 1; t < timeSteps; t++ {
		// A: root cause (independent)
		a_vals[t] = 5.0 + (rand.Float64()-0.5)*2*noiseLevel

		// B: mediator (depends on A)
		b_vals[t] = 0.8*a_vals[t-1] + 0.2*b_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel

		// C: final effect (depends on B, not directly on A)
		c_vals[t] = 0.8*b_vals[t-1] + 0.2*c_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel
	}

	for t := 0; t < timeSteps; t++ {
		dataset.Points = append(dataset.Points, DataPoint{
			Timestamp: float64(t),
			Values: map[string]float64{
				"A": a_vals[t],
				"B": b_vals[t],
				"C": c_vals[t],
			},
			Missing: make(map[string]bool),
		})
	}
	return dataset
}

/*
GenerateForkData: A → B, A → C
A is common cause of B and C.
B and C are independent conditional on A.
Phase 4 should identify A as root cause, B and C as effects (not causes).
*/
func GenerateForkData(timeSteps int, noiseLevel float64) *Dataset {
	dataset := &Dataset{
		Points: make([]DataPoint, 0),
		Nodes:  []string{"A", "B", "C"},
	}

	a_vals := make([]float64, timeSteps)
	b_vals := make([]float64, timeSteps)
	c_vals := make([]float64, timeSteps)

	a_vals[0] = 5.0 + (rand.Float64()-0.5)*2*noiseLevel
	b_vals[0] = 0.7 * a_vals[0]
	c_vals[0] = 0.6 * a_vals[0]

	for t := 1; t < timeSteps; t++ {
		// A: root cause (independent)
		a_vals[t] = 5.0 + (rand.Float64()-0.5)*2*noiseLevel

		// B: effect of A (independent effect)
		b_vals[t] = 0.7*a_vals[t-1] + 0.3*b_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel

		// C: also effect of A (independent effect)
		c_vals[t] = 0.6*a_vals[t-1] + 0.3*c_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel
	}

	for t := 0; t < timeSteps; t++ {
		dataset.Points = append(dataset.Points, DataPoint{
			Timestamp: float64(t),
			Values: map[string]float64{
				"A": a_vals[t],
				"B": b_vals[t],
				"C": c_vals[t],
			},
			Missing: make(map[string]bool),
		})
	}
	return dataset
}

/*
GenerateComplexDAGData: Z → A → B → C, Z → C, D → B
Multiple interacting causes with fork and chain structures.
- Z is confounder for A→C path
- D independently affects B
- C has multiple paths from root causes
*/
func GenerateComplexDAGData(timeSteps int, noiseLevel float64) *Dataset {
	dataset := &Dataset{
		Points: make([]DataPoint, 0),
		Nodes:  []string{"Z", "A", "B", "C", "D"},
	}

	z_vals := make([]float64, timeSteps)
	a_vals := make([]float64, timeSteps)
	b_vals := make([]float64, timeSteps)
	c_vals := make([]float64, timeSteps)
	d_vals := make([]float64, timeSteps)

	z_vals[0] = 5.0 + (rand.Float64()-0.5)*2*noiseLevel
	a_vals[0] = 0.7 * z_vals[0]
	d_vals[0] = 3.0 + (rand.Float64()-0.5)*2*noiseLevel
	b_vals[0] = 0.6*a_vals[0] + 0.5*d_vals[0]
	c_vals[0] = 0.5*b_vals[0] + 0.4*z_vals[0]

	for t := 1; t < timeSteps; t++ {
		// Z: root confounder
		z_vals[t] = 5.0 + (rand.Float64()-0.5)*2*noiseLevel

		// A: affected by Z
		a_vals[t] = 0.7*z_vals[t-1] + 0.2*a_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel

		// D: independent root cause
		d_vals[t] = 3.0 + (rand.Float64()-0.5)*2*noiseLevel

		// B: affected by both A and D
		b_vals[t] = 0.6*a_vals[t-1] + 0.5*d_vals[t-1] + 0.1*b_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel

		// C: affected by B (chain) and Z (fork)
		c_vals[t] = 0.5*b_vals[t-1] + 0.4*z_vals[t-1] + 0.1*c_vals[t-1] + (rand.Float64()-0.5)*2*noiseLevel
	}

	for t := 0; t < timeSteps; t++ {
		dataset.Points = append(dataset.Points, DataPoint{
			Timestamp: float64(t),
			Values: map[string]float64{
				"Z": z_vals[t],
				"A": a_vals[t],
				"B": b_vals[t],
				"C": c_vals[t],
				"D": d_vals[t],
			},
			Missing: make(map[string]bool),
		})
	}
	return dataset
}
