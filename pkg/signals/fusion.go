package signals

// NodeMetrics holds the fully fused signals mapped to queuing variables.
// This is the output format required by the causal pipeline.
type NodeMetrics struct {
	ArrivalRate     float64
	ServiceRate     float64
	QueueLength     float64
	ComputePressure float64
	MemoryPressure  float64
	NetworkPressure float64
	DominantSignal  string
}

// FuseSignals integrates the three independent metrics into a unified queuing model
// and dynamically calculates ServiceRate based on system capacity and network saturation.
func FuseSignals(compute ComputeSignal, memory MemorySignal, net NetworkSignal, baseline *AdaptiveBaseline) NodeMetrics {
	// Dynamically compute alpha based on historical ThrottleFraction correlation
	alpha := clamp(baseline.ThrottleCorrelation(), 0.1, 0.5)

	computePressure := compute.CPUFraction + (compute.ThrottleFraction * alpha)
	
	// Compute composite Page-Hinkley and EWMA score for memory anomaly
	memoryPressure := baseline.ComputeMemoryPressure(memory.GrowthRateMBps, memory.FaultRatePerSec)

	// Record network bytes delta for MaxBandwidth rolling window
	totalBytesDelta := net.RxBytesDelta + net.TxBytesDelta
	baseline.RecordNetworkBytesDelta(totalBytesDelta)

	maxBandwidth := baseline.GetMaxBandwidth()
	
	netUtil := 0.0
	if maxBandwidth > 0 && net.TimeDeltaSeconds > 0 {
		bytesPerSecond := float64(totalBytesDelta) / net.TimeDeltaSeconds
		netUtil = bytesPerSecond / maxBandwidth
	}
	
	networkPressure := netUtil + (baseline.Beta * net.DropRatio)

	// Determine dominant signal using dynamically computed Z-Score anomaly thresholds
	dominantSignal := determineDominant(computePressure, memoryPressure, networkPressure, baseline)

	// Fetch rolling capacity and compute dynamic ServiceRate
	baseCapacity := baseline.GetBaseCapacity()
	serviceRate := (1.0 - compute.ThrottleFraction) * baseCapacity * (1.0 - networkPressure)
	
	// Ensure service rate doesn't drop below zero to prevent queuing math issues
	if serviceRate < 0.01 {
		serviceRate = 0.01
	}

	// Update rolling windows in baseline with the newly fused signals
	baseline.RecordSignals(computePressure, memoryPressure, networkPressure, serviceRate, compute.CPUFraction, compute.ThrottleFraction)

	return NodeMetrics{
		ArrivalRate:     computePressure,
		ServiceRate:     serviceRate,
		QueueLength:     memoryPressure,
		ComputePressure: computePressure,
		MemoryPressure:  memoryPressure,
		NetworkPressure: networkPressure,
		DominantSignal:  dominantSignal,
	}
}

// determineDominant returns the signal with the highest pressure IF it exceeds its dynamic Z-score threshold.
func determineDominant(compute, memory, network float64, baseline *AdaptiveBaseline) string {
	maxVal := 0.0
	dominant := "none"

	computeThreshold := baseline.GetComputeThreshold()
	memoryThreshold := baseline.GetMemoryThreshold()
	networkThreshold := baseline.GetNetworkThreshold()

	if compute > computeThreshold && compute > maxVal {
		maxVal = compute
		dominant = "compute"
	}
	if memory > memoryThreshold && memory > maxVal {
		maxVal = memory
		dominant = "memory"
	}
	if network > networkThreshold && network > maxVal {
		maxVal = network
		dominant = "network"
	}

	return dominant
}

func clamp(val, min, max float64) float64 {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}
