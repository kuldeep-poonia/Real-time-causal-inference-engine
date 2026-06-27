package signals

import (
	"absia/pkg/docker"
	"math"
	"sync"
	"time"
)

// NetworkSnapshot stores absolute network stats for delta calculation.
type NetworkSnapshot struct {
	RxBytes   uint64
	TxBytes   uint64
	RxDropped uint64
	TxDropped uint64
	RxPackets uint64
	TxPackets uint64
	Timestamp time.Time
}

// PreviousCPU stores absolute CPU stats for delta calculation.
type PreviousCPU struct {
	TotalCPU  uint64
	SystemCPU uint64
}

// SignalStats tracks running mean and variance using Welford's online algorithm.
type SignalStats struct {
	count float64
	mean  float64
	m2    float64
}

// Update implements Welford's online algorithm
// Citation: Welford (1962) — numerically stable single-pass mean and variance computation
func (s *SignalStats) Update(value float64) {
	s.count++
	delta := value - s.mean
	s.mean += delta / s.count
	delta2 := value - s.mean
	s.m2 += delta * delta2
}

func (s *SignalStats) StdDev() float64 {
	if s.count < 2 {
		return 0
	}
	return math.Sqrt(s.m2 / (s.count - 1))
}

func (s *SignalStats) Threshold(sigmas float64) float64 {
	if s.count < 10 {
		// Not enough baseline — use research paper defaults
		return -1
	}
	return s.mean + (sigmas * s.StdDev())
}

// AdaptiveBaseline manages rolling window metrics, node state, and anomaly thresholds.
type AdaptiveBaseline struct {
	mu sync.RWMutex

	nodeID                  string
	previousNetworkSnapshot *NetworkSnapshot
	previousCPU             *PreviousCPU
	previousMemoryStats     *docker.StatsResponse

	Beta float64 // Drop ratio weight (configurable)

	computeStats SignalStats
	memoryStats  SignalStats
	networkStats SignalStats

	// Rolling window (size 20) for each signal
	computePressures  []float64
	memoryPressures   []float64
	networkPressures  []float64
	serviceRates      []float64
	cpuFractions      []float64
	throttleFractions []float64

	// MaxBandwidth requires 10 intervals of bytes delta
	networkBytesDeltas []uint64
}

// NewAdaptiveBaseline creates a new baseline manager for a specific node.
func NewAdaptiveBaseline(nodeID string) *AdaptiveBaseline {
	return &AdaptiveBaseline{
		nodeID:             nodeID,
		Beta:               2.0, // Research constant default
		computePressures:   make([]float64, 0, 20),
		memoryPressures:    make([]float64, 0, 20),
		networkPressures:   make([]float64, 0, 20),
		serviceRates:       make([]float64, 0, 20),
		cpuFractions:       make([]float64, 0, 20),
		throttleFractions:  make([]float64, 0, 20),
		networkBytesDeltas: make([]uint64, 0, 10),
	}
}

// UpdateNetwork stores the current snapshot and returns the previous one (if any) for delta computation.
func (ab *AdaptiveBaseline) UpdateNetwork(snapshot NetworkSnapshot) *NetworkSnapshot {
	ab.mu.Lock()
	defer ab.mu.Unlock()

	prev := ab.previousNetworkSnapshot
	ab.previousNetworkSnapshot = &snapshot
	return prev
}

// UpdateCPU stores the current CPU stats and returns the previous one (if any) for delta computation.
func (ab *AdaptiveBaseline) UpdateCPU(total, system uint64) *PreviousCPU {
	ab.mu.Lock()
	defer ab.mu.Unlock()

	prev := ab.previousCPU
	ab.previousCPU = &PreviousCPU{
		TotalCPU:  total,
		SystemCPU: system,
	}
	return prev
}

// UpdateMemoryStats stores the current memory stats and returns the previous one for page fault deltas.
func (ab *AdaptiveBaseline) UpdateMemoryStats(stats *docker.StatsResponse) *docker.StatsResponse {
	ab.mu.Lock()
	defer ab.mu.Unlock()

	prev := ab.previousMemoryStats
	ab.previousMemoryStats = stats
	return prev
}

// RecordNetworkBytesDelta adds a network bytes delta to the 10-interval rolling window.
func (ab *AdaptiveBaseline) RecordNetworkBytesDelta(deltaBytes uint64) {
	ab.mu.Lock()
	defer ab.mu.Unlock()

	ab.networkBytesDeltas = append(ab.networkBytesDeltas, deltaBytes)
	if len(ab.networkBytesDeltas) > 10 {
		ab.networkBytesDeltas = ab.networkBytesDeltas[1:]
	}
}

// GetMaxBandwidth computes the rolling max of the last 10 intervals * 1.5.
// If fewer than 3 baseline samples exist, returns 0.0 to prevent false network alerts on startup.
func (ab *AdaptiveBaseline) GetMaxBandwidth() float64 {
	ab.mu.RLock()
	defer ab.mu.RUnlock()

	if len(ab.networkBytesDeltas) < 3 {
		return 0.0
	}

	var max uint64
	for _, val := range ab.networkBytesDeltas {
		if val > max {
			max = val
		}
	}
	return float64(max) * 1.5
}

// IsHealthy evaluates if the system is currently healthy based on the latest pressure signals.
// Healthy = ComputePressure < 0.3 AND MemoryPressure < 0.4 AND NetworkPressure < 0.1
func (ab *AdaptiveBaseline) IsHealthy(computePressure, memoryPressure, networkPressure float64) bool {
	return computePressure < 0.3 && memoryPressure < 0.4 && networkPressure < 0.1
}

// RecordSignals stores the current signals into the rolling windows and stats accumulators.
func (ab *AdaptiveBaseline) RecordSignals(compute, memory, network, serviceRate, cpuFraction, throttleFraction float64) {
	ab.mu.Lock()
	defer ab.mu.Unlock()

	ab.computePressures = appendWindow(ab.computePressures, compute, 20)
	ab.memoryPressures = appendWindow(ab.memoryPressures, memory, 20)
	ab.networkPressures = appendWindow(ab.networkPressures, network, 20)
	ab.cpuFractions = appendWindow(ab.cpuFractions, cpuFraction, 20)
	ab.throttleFractions = appendWindow(ab.throttleFractions, throttleFraction, 20)

	ab.computeStats.Update(compute)
	ab.memoryStats.Update(memory)
	ab.networkStats.Update(network)

	if compute < 0.3 && memory < 0.4 && network < 0.1 {
		ab.serviceRates = appendWindow(ab.serviceRates, serviceRate, 20)
	}
}

// GetBaseCapacity returns the rolling mean of ServiceRate from the last 20 healthy samples.
func (ab *AdaptiveBaseline) GetBaseCapacity() float64 {
	ab.mu.RLock()
	defer ab.mu.RUnlock()

	if len(ab.serviceRates) == 0 {
		return 1.0 // Seed equivalent to original hardcoded behavior
	}

	var sum float64
	for _, val := range ab.serviceRates {
		sum += val
	}
	return sum / float64(len(ab.serviceRates))
}

// ThrottleCorrelation computes the Pearson correlation coefficient between CPU fraction and throttle fraction.
// Citation: Standard signal correlation metric.
func (ab *AdaptiveBaseline) ThrottleCorrelation() float64 {
	ab.mu.RLock()
	defer ab.mu.RUnlock()
	
	n := float64(len(ab.cpuFractions))
	if n < 2 {
		return 0.1
	}
	
	sumX, sumY, sumXY, sumX2, sumY2 := 0.0, 0.0, 0.0, 0.0, 0.0
	for i := 0; i < int(n); i++ {
		x := ab.cpuFractions[i]
		y := ab.throttleFractions[i]
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
		sumY2 += y * y
	}
	
	numerator := (n * sumXY) - (sumX * sumY)
	denominator := math.Sqrt((n*sumX2 - sumX*sumX) * (n*sumY2 - sumY*sumY))
	
	if denominator == 0 {
		return 0.1
	}
	
	return numerator / denominator
}

// GetComputeThreshold calculates dynamic anomaly threshold or falls back to constant.
// Dynamic value = mean + 3σ (3 standard deviations)
// Reference: FIRM paper uses 3σ for metric anomaly classification in Section 5.1.
func (ab *AdaptiveBaseline) GetComputeThreshold() float64 {
	ab.mu.RLock()
	defer ab.mu.RUnlock()
	t := ab.computeStats.Threshold(3.0)
	if t < 0 {
		return 0.7
	}
	return t
}

func (ab *AdaptiveBaseline) GetMemoryThreshold() float64 {
	ab.mu.RLock()
	defer ab.mu.RUnlock()
	t := ab.memoryStats.Threshold(3.0)
	if t < 0 {
		return 0.6
	}
	return t
}

func (ab *AdaptiveBaseline) GetNetworkThreshold() float64 {
	ab.mu.RLock()
	defer ab.mu.RUnlock()
	t := ab.networkStats.Threshold(3.0)
	if t < 0 {
		return 0.3
	}
	return t
}

func appendWindow(slice []float64, val float64, max int) []float64 {
	slice = append(slice, val)
	if len(slice) > max {
		return slice[1:]
	}
	return slice
}
