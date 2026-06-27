package signals

import (
	"absia/pkg/docker"
)

// ComputeSignal encapsulates the CPU and throttling metrics.
type ComputeSignal struct {
	CPUFraction      float64
	ThrottleFraction float64
}

// CollectCompute extracts CPU and throttling metrics from Docker stats.
// Citation: "Microscope: Pinpoint Performance Issues with Causal Graphs in Micro-service Environments"
// Justification: Throttling metrics identify when CPU limits are capping performance, which creates
// artificial queuing (ArrivalRate saturation) before raw utilization hits 100%.
func CollectCompute(cur *docker.StatsResponse, prev *PreviousCPU) ComputeSignal {
	cpuFraction := 0.0

	// Calculate CPU Fraction using the delta method
	if prev != nil {
		cpuDelta := float64(cur.CPUStats.CPUUsage.TotalUsage) - float64(prev.TotalCPU)
		sysDelta := float64(cur.CPUStats.SystemCPUUsage) - float64(prev.SystemCPU)

		if sysDelta > 0 && cpuDelta >= 0 {
			numCPUs := cur.CPUStats.OnlineCPUs
			if numCPUs == 0 {
				numCPUs = len(cur.CPUStats.CPUUsage.PercpuUsage)
			}
			if numCPUs == 0 {
				numCPUs = 1
			}

			cpuFraction = (cpuDelta / sysDelta) * float64(numCPUs)
			if cpuFraction > 1.0 {
				cpuFraction = 1.0
			}
		}
	}

	// Calculate Throttle Fraction
	throttleFraction := 0.0
	throttledPeriods := cur.CPUStats.ThrottlingData.ThrottledPeriods
	totalPeriods := cur.CPUStats.ThrottlingData.Periods
	
	if totalPeriods > 0 {
		throttleFraction = float64(throttledPeriods) / float64(totalPeriods)
		if throttleFraction > 1.0 {
			throttleFraction = 1.0
		}
	}

	return ComputeSignal{
		CPUFraction:      cpuFraction,
		ThrottleFraction: throttleFraction,
	}
}
