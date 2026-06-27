package signals

import (
	"absia/pkg/docker"
)

// NetworkSignal encapsulates the aggregated network deltas and saturation metrics.
type NetworkSignal struct {
	RxBytesDelta   uint64
	TxBytesDelta   uint64
	RxPacketsDelta uint64
	TxPacketsDelta uint64
	RxDroppedDelta   uint64
	TxDroppedDelta   uint64
	DropRatio        float64
	TimeDeltaSeconds float64
}

// CollectNetwork extracts network I/O deltas and dropped packets across all interfaces.
// Citation: "ROOT-CAUSE ANALYSIS OF NETWORK ANOMALIES IN CARRIER NETWORKS" (IEEE)
// Justification: Single snapshots of network counters are meaningless; we must aggregate deltas 
// across all interfaces and compute a drop ratio to accurately measure network saturation.
func CollectNetwork(cur *docker.StatsResponse, prev *NetworkSnapshot, timeDeltaSeconds float64) NetworkSignal {
	if timeDeltaSeconds <= 0 {
		timeDeltaSeconds = 15.0 // default collection interval
	}

	// Aggregate current totals across ALL interfaces (not just eth0)
	var curRxBytes, curTxBytes, curRxPackets, curTxPackets, curRxDropped, curTxDropped uint64
	for _, netStats := range cur.Networks {
		curRxBytes += netStats.RxBytes
		curTxBytes += netStats.TxBytes
		curRxPackets += netStats.RxPackets
		curTxPackets += netStats.TxPackets
		curRxDropped += netStats.RxDropped
		curTxDropped += netStats.TxDropped
	}

	sig := NetworkSignal{}

	// Compute deltas if previous snapshot exists
	if prev != nil {
		if curRxBytes > prev.RxBytes {
			sig.RxBytesDelta = curRxBytes - prev.RxBytes
		}
		if curTxBytes > prev.TxBytes {
			sig.TxBytesDelta = curTxBytes - prev.TxBytes
		}
		if curRxPackets > prev.RxPackets {
			sig.RxPacketsDelta = curRxPackets - prev.RxPackets
		}
		if curTxPackets > prev.TxPackets {
			sig.TxPacketsDelta = curTxPackets - prev.TxPackets
		}
		if curRxDropped > prev.RxDropped {
			sig.RxDroppedDelta = curRxDropped - prev.RxDropped
		}
		if curTxDropped > prev.TxDropped {
			sig.TxDroppedDelta = curTxDropped - prev.TxDropped
		}
	}

	// Handles zero-packet case safely (no divide by zero)
	totalPackets := sig.RxPacketsDelta + sig.TxPacketsDelta
	if totalPackets > 0 {
		sig.DropRatio = float64(sig.RxDroppedDelta+sig.TxDroppedDelta) / float64(totalPackets)
	} else {
		sig.DropRatio = 0.0
	}

	sig.TimeDeltaSeconds = timeDeltaSeconds
	return sig
}
