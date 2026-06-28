package adaptive

// ThresholdResult contains structured information about the threshold check.
type ThresholdResult struct {
	Value      float64 // The calculated threshold value
	IsAnomaly  bool    // True if the current value is anomalous
	Confidence float64 // 0.0-1.0 confidence in this threshold (based on warmup)
	IsWarmup   bool    // True if still in warmup phase (fallback defaults apply)
}

// ThresholdStrategy defines how thresholds are calculated.
type ThresholdStrategy interface {
	// Evaluate takes the metric's WelfordState, current value, and whether higher is worse.
	// Returns a structured ThresholdResult.
	Evaluate(state *WelfordState, currentValue float64, higherIsWorse bool) ThresholdResult
}

// ThreeSigmaStrategy uses the 3-sigma rule (Shewhart, 1931).
// Citation: Montgomery (2009) "Introduction to Statistical Quality Control" Chapter 4
type ThreeSigmaStrategy struct {
	SigmaK          float64
	MinObservations int64
	FallbackValue   float64
	VarianceFloor   float64 // Minimum standard deviation to avoid zero-variance collapse
}

// Evaluate implements the ThresholdStrategy.
func (s *ThreeSigmaStrategy) Evaluate(state *WelfordState, currentValue float64, higherIsWorse bool) ThresholdResult {
	count := state.Count()
	if count < s.MinObservations {
		// Warmup phase: use conservative fallback.
		isAnomaly := false
		if higherIsWorse && currentValue > s.FallbackValue {
			isAnomaly = true
		} else if !higherIsWorse && currentValue < s.FallbackValue {
			isAnomaly = true
		}
		
		confidence := 0.0
		if s.MinObservations > 0 {
			confidence = float64(count) / float64(s.MinObservations)
		}
		
		return ThresholdResult{
			Value:      s.FallbackValue,
			IsAnomaly:  isAnomaly,
			Confidence: confidence,
			IsWarmup:   true,
		}
	}

	mean := state.Mean()
	stddev := state.StdDev()
	
	if stddev < s.VarianceFloor {
		stddev = s.VarianceFloor
	}
	
	var threshold float64
	var isAnomaly bool
	
	if higherIsWorse {
		threshold = mean + s.SigmaK*stddev
		if currentValue > threshold {
			isAnomaly = true
		}
	} else {
		threshold = mean - s.SigmaK*stddev
		if currentValue < threshold {
			isAnomaly = true
		}
	}

	return ThresholdResult{
		Value:      threshold,
		IsAnomaly:  isAnomaly,
		Confidence: 1.0,
		IsWarmup:   false,
	}
}
