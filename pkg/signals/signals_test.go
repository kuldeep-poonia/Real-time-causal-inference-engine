package signals

import (
	"absia/pkg/docker"
	"math"
	"testing"
)

// Helper to fill baseline with identical samples to test Z-score logic
func fillBaseline(ab *AdaptiveBaseline, val float64) {
	for i := 0; i < 15; i++ {
		ab.recordSignalForTest(val, val, val)
	}
}

// 1. Dynamic threshold calculation after 10+ samples
func TestDynamicThreshold_Above10Samples(t *testing.T) {
	ab := NewAdaptiveBaseline("node1")
	fillBaseline(ab, 0.2) // Mean 0.2, StdDev 0
	
	// Because variance is 0, stddev is 0. Threshold should be precisely 0.2
	thresh := ab.GetComputeThreshold()
	if thresh != 0.2 {
		t.Errorf("Expected threshold 0.2 for zero variance, got %f", thresh)
	}
	
	// Add variance
	ab.recordSignalForTest(0.4, 0.4, 0.4)
	thresh2 := ab.GetComputeThreshold()
	if thresh2 <= 0.2 || thresh2 > 0.5 {
		t.Errorf("Expected dynamic threshold with 3-sigma to be sane, got %f", thresh2)
	}
}

// 2. Fallback to constants when count < 10
func TestFallbackThreshold_Below10Samples(t *testing.T) {
	ab := NewAdaptiveBaseline("node1")
	ab.recordSignalForTest(0.1, 0.1, 0.1) // Only 1 sample, count < 10
	
	thresh := ab.GetComputeThreshold()
	if thresh != 0.7 {
		t.Errorf("Expected fallback 0.7, got %f", thresh)
	}
}

// 3. Welford algorithm numerical stability test
func TestWelfordAlgorithmStability(t *testing.T) {
	s := SignalStats{}
	// Large values that could cause precision loss in naive variance sums
	large := 1e8
	for i := 0; i < 10; i++ {
		s.Update(large + float64(i))
	}
	// Mean should be precisely 1e8 + 4.5
	if math.Abs(s.mean-(large+4.5)) > 1e-5 {
		t.Errorf("Welford mean unstable: expected %f, got %f", large+4.5, s.mean)
	}
	// StdDev of {0..9} is ~3.02765
	if math.Abs(s.StdDev()-3.02765) > 1e-4 {
		t.Errorf("Welford stddev unstable: expected ~3.02765, got %f", s.StdDev())
	}
}

// 4. Z-score threshold correctly identifies anomaly
func TestZScoreIdentifiesAnomaly(t *testing.T) {
	ab := NewAdaptiveBaseline("node1")
	for i := 0; i < 20; i++ {
		ab.recordSignalForTest(0.2, 0.2, 0.2)
	}
	ab.recordSignalForTest(0.3, 0.2, 0.2) // Create a tiny stddev
	
	dom := determineDominant(0.8, 0.0, 0.0, ab)
	if dom != "compute" {
		t.Errorf("Z-score threshold failed to identify 0.8 as compute anomaly. Dom: %s", dom)
	}
}

// 5. alpha correlation computation
func TestAlphaCorrelationComputation(t *testing.T) {
	ab := NewAdaptiveBaseline("node1")
	
	// Perfect correlation
	cpu := make([]float64, 0, 10)
	throttle := make([]float64, 0, 10)
	for i := 0; i < 10; i++ {
		val := float64(i) / 10.0
		cpu = append(cpu, val)
		throttle = append(throttle, val)
	}
	ab.seedCorrelationData(cpu, throttle)
	
	corr := ab.ThrottleCorrelation()
	if math.Abs(corr-1.0) > 1e-5 {
		t.Errorf("Expected correlation 1.0, got %f", corr)
	}
	
	alpha := clamp(corr, 0.1, 0.5)
	if alpha != 0.5 {
		t.Errorf("Expected alpha 0.5 (clamped max), got %f", alpha)
	}
}

// --- All existing 8 test cases from before ---

// 6. FuseSignals with compute=0.8 → dominant="compute"
func TestFuseSignals_DominantCompute(t *testing.T) {
	baseline := NewAdaptiveBaseline("node1") // Uses fallback threshold = 0.7
	compute := ComputeSignal{CPUFraction: 0.8, ThrottleFraction: 0.0}
	memory := MemorySignal{WorkingSet: 100, Limit: 1000, MajorPageFaultRate: 0, FaultRatePerSec: 0, GrowthRateMBps: 0}
	net := NetworkSignal{}

	metrics := FuseSignals(compute, memory, net, baseline)
	if metrics.DominantSignal != "compute" {
		t.Errorf("Expected dominant 'compute', got '%s'", metrics.DominantSignal)
	}
}

// 7. FuseSignals with memory thrashing → dominant="memory"
func TestFuseSignals_DominantMemoryThrashing(t *testing.T) {
	baseline := NewAdaptiveBaseline("node1") // Uses fallback threshold = 0.6
	
	// Seed baseline to pass the cold start guard (phCount >= 3)
	baseline.ComputeMemoryPressure(0, 0)
	baseline.ComputeMemoryPressure(0, 0)
	baseline.ComputeMemoryPressure(0, 0)

	compute := ComputeSignal{CPUFraction: 0.1, ThrottleFraction: 0.0}
	memory := MemorySignal{WorkingSet: 900, Limit: 1000, MajorPageFaultRate: 100, FaultRatePerSec: 100, GrowthRateMBps: 50}
	net := NetworkSignal{} 

	metrics := FuseSignals(compute, memory, net, baseline)
	if metrics.DominantSignal != "memory" {
		t.Errorf("Expected dominant 'memory', got '%s'", metrics.DominantSignal)
	}
}

// 8. CollectNetwork with nil prev → all deltas = 0
func TestCollectNetwork_NilPrev(t *testing.T) {
	cur := &docker.StatsResponse{
		Networks: map[string]docker.NetworkStats{
			"eth0": {RxBytes: 1000, TxBytes: 500, RxPackets: 10, TxPackets: 5},
		},
	}
	sig := CollectNetwork(cur, nil, 15.0)
	if sig.RxBytesDelta != 0 || sig.DropRatio != 0.0 {
		t.Errorf("Expected 0 deltas for nil prev, got %+v", sig)
	}
}

// 9. CollectNetwork with zero packets → DropRatio = 0.0
func TestCollectNetwork_ZeroPackets(t *testing.T) {
	cur := &docker.StatsResponse{
		Networks: map[string]docker.NetworkStats{
			"eth0": {RxBytes: 1000, TxBytes: 500, RxPackets: 10, TxPackets: 5},
		},
	}
	prev := &NetworkSnapshot{
		RxBytes: 1000, TxBytes: 500, RxPackets: 10, TxPackets: 5,
	}
	sig := CollectNetwork(cur, prev, 15.0)
	if sig.DropRatio != 0.0 {
		t.Errorf("Expected DropRatio 0.0, got %f", sig.DropRatio)
	}
}

// 10. GetMaxBandwidth with < 3 samples → returns 0.0
func TestGetMaxBandwidth_UnderThreeSamples(t *testing.T) {
	baseline := NewAdaptiveBaseline("node1")
	baseline.RecordNetworkBytesDelta(1000)
	baseline.RecordNetworkBytesDelta(2000)
	maxBw := baseline.GetMaxBandwidth()
	if maxBw != 0.0 {
		t.Errorf("Expected MaxBandwidth 0.0 for <3 samples, got %f", maxBw)
	}
}

// 11. determineDominant all below threshold → "none"
func TestDetermineDominant_None(t *testing.T) {
	ab := NewAdaptiveBaseline("node1") // Fallback thresholds
	dom := determineDominant(0.2, 0.3, 0.1, ab)
	if dom != "none" {
		t.Errorf("Expected dominant 'none', got '%s'", dom)
	}
}

// 12. GetBaseCapacity empty baseline → returns 1.0
func TestGetBaseCapacity_EmptyBaseline(t *testing.T) {
	baseline := NewAdaptiveBaseline("node1")
	cap := baseline.GetBaseCapacity()
	if cap != 1.0 {
		t.Errorf("Expected BaseCapacity 1.0, got %f", cap)
	}
}
