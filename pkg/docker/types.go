// Package docker provides a minimal, stdlib-only client for the Docker Engine API
// over the Unix socket at /var/run/docker.sock.
//
// No external dependencies. Uses net/http with a custom DialContext that speaks
// over the unix socket. This replaces github.com/docker/docker/client entirely.
package docker

// Container is the subset of fields returned by GET /containers/json.
type Container struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
	Image string   `json:"Image"`
	State string   `json:"State"`
}

// StatsResponse is the response from GET /containers/{id}/stats?stream=false.
// Only fields required for CPU% and memory% calculation are declared.
type StatsResponse struct {
	Read        string   `json:"read"`
	CPUStats    CPUStats `json:"cpu_stats"`
	PreCPUStats CPUStats `json:"precpu_stats"`
	MemoryStats MemStats `json:"memory_stats"`
}

// CPUStats holds CPU accounting data from the Docker stats endpoint.
type CPUStats struct {
	CPUUsage struct {
		TotalUsage  uint64   `json:"total_usage"`
		PercpuUsage []uint64 `json:"percpu_usage"` // nil on cgroups v2
	} `json:"cpu_usage"`
	SystemCPUUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs     int    `json:"online_cpus"` // 0 on older daemons; fall back to len(PercpuUsage)
}

// MemStats holds memory accounting data from the Docker stats endpoint.
type MemStats struct {
	Usage uint64            `json:"usage"` // includes page cache on cgroups v1
	Limit uint64            `json:"limit"`
	Stats map[string]uint64 `json:"stats"` // keyed e.g. "cache", "inactive_file"
}

// CPUPercent computes the CPU utilisation fraction [0,1] from a stats snapshot
// and its predecessor (required for the delta calculation).
// Returns 0.0 when there is insufficient data (first sample, or zero deltas).
func CPUPercent(cur, prev *StatsResponse) float64 {
	cpuDelta := float64(cur.CPUStats.CPUUsage.TotalUsage) -
		float64(prev.CPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(cur.CPUStats.SystemCPUUsage) -
		float64(prev.CPUStats.SystemCPUUsage)

	if sysDelta <= 0 || cpuDelta < 0 {
		return 0.0
	}

	numCPUs := cur.CPUStats.OnlineCPUs
	if numCPUs == 0 {
		numCPUs = len(cur.CPUStats.CPUUsage.PercpuUsage)
	}
	if numCPUs == 0 {
		numCPUs = 1
	}

	frac := (cpuDelta / sysDelta) * float64(numCPUs)
	if frac > 1.0 {
		return 1.0
	}
	return frac
}

// MemPercent computes the memory utilisation fraction [0,1].
// Subtracts page-cache (inactive_file > cache fallback) from usage so that
// the metric reflects the working set, not the OS page cache.
func MemPercent(s *StatsResponse) float64 {
	if s.MemoryStats.Limit == 0 {
		return 0.0
	}
	usage := s.MemoryStats.Usage
	// cgroups v2 uses "inactive_file"; v1 uses "cache"
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok && usage > v {
		usage -= v
	} else if v, ok := s.MemoryStats.Stats["cache"]; ok && usage > v {
		usage -= v
	}
	frac := float64(usage) / float64(s.MemoryStats.Limit)
	if frac > 1.0 {
		return 1.0
	}
	if frac < 0 {
		return 0.0
	}
	return frac
}