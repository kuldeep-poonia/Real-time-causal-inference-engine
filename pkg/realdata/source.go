
package realdata

import (
	"fmt"

	phase1 "absia/internal/intelligence/phase1_signal"
	"absia/pkg/data"
)

const defaultFeatureIndex = 0 // "value" is always at index 0 (single-feature schema)

// GetDataset converts the current state of all active Processors in the Manager
// into a data.Dataset.
//
// Contract:
//   - Each unique signal ID in the Manager becomes one causal-graph node.
//   - Raw (un-normalised) values are used so that causal algorithms work on
//     the original metric scale, not the z-scored internal representation.
//   - Points with no raw data at time t are marked Missing.
//   - Returns an error when fewer than minPoints time-series rows exist for
//     ANY node — the caller should fall back to synthetic data.
func GetDataset(m *phase1.Manager, minPoints int) (*data.Dataset, error) {
	ids := m.GetSignalIDs()
	if len(ids) == 0 {
		return nil, fmt.Errorf("realdata: manager has no active processors — Prometheus may not have delivered data yet")
	}

	// Snapshot all matrices while holding no locks (GetMatrix acquires its own).
	type snapshot struct {
		id  string
		mat phase1.Matrix
	}
	snaps := make([]snapshot, 0, len(ids))
	maxLen := 0

	for _, id := range ids {
		proc := m.GetProcessor(id)
		mat := proc.GetMatrix()
		if len(mat.RawValues) > maxLen {
			maxLen = len(mat.RawValues)
		}
		snaps = append(snaps, snapshot{id: id, mat: mat})
	}

	if maxLen < minPoints {
		return nil, fmt.Errorf(
			"realdata: only %d data points collected (need %d) — waiting for more Prometheus scrapes",
			maxLen, minPoints,
		)
	}

	// Build node list in stable order (GetSignalIDs returns sorted IDs).
	nodeIDs := make([]string, len(snaps))
	for i, s := range snaps {
		nodeIDs[i] = s.id
	}

	dataset := &data.Dataset{
		Nodes:  nodeIDs,
		Points: make([]data.DataPoint, maxLen),
	}

	for t := 0; t < maxLen; t++ {
		pt := data.DataPoint{
			Timestamp: float64(t),
			Values:    make(map[string]float64),
			Missing:   make(map[string]bool),
		}
		for _, s := range snaps {
			if t < len(s.mat.RawValues) && len(s.mat.RawValues[t]) > defaultFeatureIndex {
				pt.Values[s.id] = s.mat.RawValues[t][defaultFeatureIndex]
			} else {
				pt.Missing[s.id] = true
				// Carry-forward: reuse last known value so downstream
				// lag-correlation functions don't see gaps.
				if t > 0 {
					if prev, ok := dataset.Points[t-1].Values[s.id]; ok {
						pt.Values[s.id] = prev
						pt.Missing[s.id] = false
					}
				}
			}
		}
		dataset.Points[t] = pt
	}

	return dataset, nil
}

// NodeStatesFromManager extracts per-node M/M/1 state variables from a Manager.
// Returns a map keyed by signal ID, usable as phase3.NodeState (caller converts).
//
// These states include derived λ, μ, ρ, L, and W values from the signal
// processor's inter-arrival and derivative-variance estimators.
func NodeStatesFromManager(m *phase1.Manager) map[string]phase1.NodeState {
	ids := m.GetSignalIDs()
	out := make(map[string]phase1.NodeState, len(ids))
	for _, id := range ids {
		proc := m.GetProcessor(id)
		out[id] = proc.GetNodeState()
	}
	return out
}
