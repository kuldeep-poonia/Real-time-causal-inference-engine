package signals

import (
	"absia/pkg/docker"
)

// MemorySignal encapsulates the memory usage and page fault metrics.
type MemorySignal struct {
	WorkingSet         float64
	Limit              float64
	MajorPageFaultRate float64
}

// CollectMemory extracts working set and page fault metrics from Docker stats.
// Citation: "MicroRCA: Root Cause Localization of Performance Issues in Microservices" (NOMS 2020)
// Justification: Raw memory usage is insufficient for anomaly detection. Page fault rates 
// and active working set size are more reliable causal indicators of true memory exhaustion 
// (e.g., GC thrashing, swap).
func CollectMemory(cur *docker.StatsResponse, prev *docker.StatsResponse, timeDeltaSeconds float64) MemorySignal {
	limit := float64(cur.MemoryStats.Limit)
	if limit == 0 {
		limit = 1.0 // Prevent division by zero later
	}

	// Computes: WorkingSet = active_anon (not raw usage)
	workingSet := 0.0
	if v, ok := cur.MemoryStats.Stats["active_anon"]; ok {
		workingSet = float64(v)
	}
	
	if workingSet == 0 {
		cache := uint64(0)
		if v, ok := cur.MemoryStats.Stats["cache"]; ok {
			cache = v
		}
		if cur.MemoryStats.Usage > cache {
			workingSet = float64(cur.MemoryStats.Usage - cache)
		}
	}

	// Computes: MajorPageFaultRate = delta(pgmajfault) per second since last collection
	majorPageFaultRate := 0.0
	if prev != nil && timeDeltaSeconds > 0 {
		curFaults := uint64(0)
		if v, ok := cur.MemoryStats.Stats["pgmajfault"]; ok {
			curFaults = v
		}

		prevFaults := uint64(0)
		if v, ok := prev.MemoryStats.Stats["pgmajfault"]; ok {
			prevFaults = v
		}

		if curFaults > prevFaults {
			majorPageFaultRate = float64(curFaults-prevFaults) / timeDeltaSeconds
		}
	}

	return MemorySignal{
		WorkingSet:         workingSet,
		Limit:              limit,
		MajorPageFaultRate: majorPageFaultRate,
	}
}
