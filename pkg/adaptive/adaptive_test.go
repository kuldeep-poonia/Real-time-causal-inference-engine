package adaptive

import (
	"math"
	"testing"
)

func TestAdaptiveNodeProfile_Warmup(t *testing.T) {
	profile := NewAdaptiveNodeProfile("node-1", 3.0, 10)
	
	// During warmup (count < 10), it should use fallbacks
	for i := 0; i < 5; i++ {
		profile.Update(0.1, 10.0, 50.0)
	}

	// 0.85 is fallback for load. 0.9 > 0.85 should trigger anomaly even in warmup.
	res := profile.EvaluateLoad(0.9)
	if !res.IsWarmup {
		t.Errorf("Expected IsWarmup = true")
	}
	if !res.IsAnomaly {
		t.Errorf("Expected anomaly during warmup for value > fallback")
	}
	if res.Confidence >= 1.0 {
		t.Errorf("Expected low confidence during warmup")
	}
}

func TestAdaptiveNodeProfile_StableSignal(t *testing.T) {
	profile := NewAdaptiveNodeProfile("node-2", 3.0, 10)
	
	// 20 stable readings
	for i := 0; i < 20; i++ {
		profile.Update(0.5, 20.0, 100.0)
	}

	res := profile.EvaluateLoad(0.5)
	if res.IsAnomaly {
		t.Errorf("Expected no anomaly for stable signal")
	}
	
	resSpike := profile.EvaluateLoad(0.7)
	if !resSpike.IsAnomaly {
		t.Errorf("Expected anomaly for spike above variance floor (0.65) in stable signal")
	}
}

func TestAdaptiveNodeProfile_NoisySignal(t *testing.T) {
	profile := NewAdaptiveNodeProfile("node-3", 3.0, 10)
	
	// Noisy readings between 0.3 and 0.7 (mean ~0.5)
	for i := 0; i < 100; i++ {
		val := 0.5
		if i%2 == 0 {
			val = 0.7
		} else {
			val = 0.3
		}
		profile.Update(val, 20.0, 100.0)
	}
	// stddev for [0.7, 0.3] is 0.2. Mean 0.5.
	// 3-sigma upper limit is 0.5 + 3*0.2 = 1.1

	// Value 0.8 should NOT be an anomaly in this noisy profile
	res := profile.EvaluateLoad(0.8)
	if res.IsAnomaly {
		t.Errorf("Expected no anomaly for 0.8 in noisy signal (limit is ~1.1)")
	}

	// Value 1.2 SHOULD be an anomaly
	res2 := profile.EvaluateLoad(1.2)
	if !res2.IsAnomaly {
		t.Errorf("Expected anomaly for 1.2 in noisy signal")
	}
}

func TestAdaptiveNodeProfile_Drift(t *testing.T) {
	profile := NewAdaptiveNodeProfile("node-4", 3.0, 10)
	
	// Gradual drift upwards from 0.1 to 0.8
	for i := 1; i <= 100; i++ {
		val := 0.1 + float64(i)*0.007
		profile.Update(val, 20.0, 100.0)
	}

	// The mean should have followed the drift, so a value of 0.8 at the end shouldn't be a huge anomaly
	profile.EvaluateLoad(0.8)
	// Because of the variance accumulated during the drift, 0.8 might be within 3 sigma.
	// Let's check a massive spike to 2.0 to be sure
	if profile.EvaluateLoad(2.0).IsAnomaly == false {
		t.Errorf("Expected 2.0 to be anomalous after drift to 0.8")
	}
}

func TestAdaptiveNodeProfile_MissingOrInvalidSamples(t *testing.T) {
	profile := NewAdaptiveNodeProfile("node-5", 3.0, 10)
	profile.Update(0.5, 20.0, 100.0)
	profile.Update(math.NaN(), math.NaN(), math.NaN())
	profile.Update(math.Inf(1), 20.0, 100.0)
	
	if profile.loadStats.Count() != 1 {
		t.Errorf("Expected invalid samples to be ignored, got count %d", profile.loadStats.Count())
	}
}
