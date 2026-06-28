package adaptive

import (
	"sync"
)

// AdaptiveNodeProfile holds per-node learned thresholds.
// All values are derived from the node's own observation history.
type AdaptiveNodeProfile struct {
	mu     sync.RWMutex
	nodeID string

	// Welford state for each metric
	loadStats    *WelfordState
	queueStats   *WelfordState
	latencyStats *WelfordState

	// Guard metrics
	corrNoPathStats   *WelfordState
	residualStats     *WelfordState
	instabilityStats  *WelfordState
	varianceSNRStats  *WelfordState

	// Strategies
	loadStrategy    ThresholdStrategy
	queueStrategy   ThresholdStrategy
	latencyStrategy ThresholdStrategy
	
	corrNoPathStrategy  ThresholdStrategy
	residualStrategy    ThresholdStrategy
	instabilityStrategy ThresholdStrategy
	varianceSNRStrategy ThresholdStrategy
}

// NewAdaptiveNodeProfile initializes a profile with 3-sigma strategies.
func NewAdaptiveNodeProfile(nodeID string, sigmaK float64, minObs int64) *AdaptiveNodeProfile {
	return &AdaptiveNodeProfile{
		nodeID:       nodeID,
		loadStats:    &WelfordState{},
		queueStats:   &WelfordState{},
		latencyStats: &WelfordState{},
		corrNoPathStats:   &WelfordState{},
		residualStats:     &WelfordState{},
		instabilityStats:  &WelfordState{},
		varianceSNRStats:  &WelfordState{},
		loadStrategy: &ThreeSigmaStrategy{
			SigmaK:          sigmaK,
			MinObservations: minObs,
			FallbackValue:   0.85, // Current hardcoded fallback
			VarianceFloor:   0.05,
		},
		queueStrategy: &ThreeSigmaStrategy{
			SigmaK:          sigmaK,
			MinObservations: minObs,
			FallbackValue:   100.0, // Current hardcoded fallback
			VarianceFloor:   5.0,
		},
		latencyStrategy: &ThreeSigmaStrategy{
			SigmaK:          sigmaK,
			MinObservations: minObs,
			FallbackValue:   1000.0, // Milliseconds fallback
			VarianceFloor:   10.0,
		},
		corrNoPathStrategy: &ThreeSigmaStrategy{
			SigmaK:          sigmaK,
			MinObservations: minObs,
			FallbackValue:   0.70,
			VarianceFloor:   0.05,
		},
		residualStrategy: &ThreeSigmaStrategy{
			SigmaK:          sigmaK,
			MinObservations: minObs,
			FallbackValue:   0.40,
			VarianceFloor:   0.05,
		},
		instabilityStrategy: &ThreeSigmaStrategy{
			SigmaK:          sigmaK,
			MinObservations: minObs,
			FallbackValue:   0.30,
			VarianceFloor:   0.05,
		},
		varianceSNRStrategy: &ThreeSigmaStrategy{
			SigmaK:          sigmaK,
			MinObservations: minObs,
			FallbackValue:   0.25,
			VarianceFloor:   0.05,
		},
	}
}

// Update feeds new metric observations into the adaptive engine.
func (p *AdaptiveNodeProfile) Update(load, queue, latency float64) {
	p.loadStats.Update(load)
	p.queueStats.Update(queue)
	p.latencyStats.Update(latency)
}

// EvaluateLoad assesses whether the given load is anomalous.
func (p *AdaptiveNodeProfile) EvaluateLoad(currentLoad float64) ThresholdResult {
	return p.loadStrategy.Evaluate(p.loadStats, currentLoad, true)
}

// EvaluateQueue assesses whether the given queue length is anomalous.
func (p *AdaptiveNodeProfile) EvaluateQueue(currentQueue float64) ThresholdResult {
	return p.queueStrategy.Evaluate(p.queueStats, currentQueue, true)
}

// EvaluateLatency assesses whether the given latency is anomalous.
func (p *AdaptiveNodeProfile) EvaluateLatency(currentLatency float64) ThresholdResult {
	return p.latencyStrategy.Evaluate(p.latencyStats, currentLatency, true)
}

// UpdateGuardMetrics feeds new latent guard observations into the engine.
func (p *AdaptiveNodeProfile) UpdateGuardMetrics(corrNoPath, residual, instability, varianceSNR float64) {
	p.corrNoPathStats.Update(corrNoPath)
	p.residualStats.Update(residual)
	p.instabilityStats.Update(instability)
	p.varianceSNRStats.Update(varianceSNR)
}

func (p *AdaptiveNodeProfile) EvaluateCorrNoPath(val float64) ThresholdResult {
	return p.corrNoPathStrategy.Evaluate(p.corrNoPathStats, val, true)
}

// EvaluateResidual requires false for higherIsWorse because a lower residual ratio is worse (anomalous).
func (p *AdaptiveNodeProfile) EvaluateResidual(val float64) ThresholdResult {
	return p.residualStrategy.Evaluate(p.residualStats, val, false)
}

func (p *AdaptiveNodeProfile) EvaluateInstability(val float64) ThresholdResult {
	return p.instabilityStrategy.Evaluate(p.instabilityStats, val, true)
}

func (p *AdaptiveNodeProfile) EvaluateVarianceSNR(val float64) ThresholdResult {
	return p.varianceSNRStrategy.Evaluate(p.varianceSNRStats, val, true)
}
