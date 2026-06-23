package phase3_causal

import "math"

// QueueModelResult holds the calculated queue length and processing delay for a single hop.
type QueueModelResult struct {
	QueueLength float64
	Delay       float64
}

// QueueModel defines an abstraction for calculating queue lengths and delays.
type QueueModel interface {
	Name() string
	Calculate(lambda, mu float64, isOverloaded bool, hopCount int, state NodeState) QueueModelResult
}

// MM1QueueModel implements the standard M/M/1 continuous queue model.
type MM1QueueModel struct{}

func (m *MM1QueueModel) Name() string { return "M/M/1" }

func (m *MM1QueueModel) Calculate(lambda, mu float64, isOverloaded bool, hopCount int, state NodeState) QueueModelResult {
	if isOverloaded {
		overflow := lambda - mu
		qLen := overflow * math.Max(1, float64(hopCount+1))
		return QueueModelResult{QueueLength: qLen, Delay: qLen / mu}
	}
	rho := lambda / mu
	if rho > 0 {
		qLen := rho / (1.0 - rho)
		delay := 0.0
		if lambda > 1e-9 {
			delay = qLen / lambda
		}
		return QueueModelResult{QueueLength: qLen, Delay: delay}
	}
	return QueueModelResult{QueueLength: 0, Delay: 0}
}

// KingmanGG1QueueModel implements the Kingman G/G/1 approximation using burstiness instrumentation.
type KingmanGG1QueueModel struct{}

func (k *KingmanGG1QueueModel) Name() string { return "G/G/1 (Kingman)" }

func (k *KingmanGG1QueueModel) Calculate(lambda, mu float64, isOverloaded bool, hopCount int, state NodeState) QueueModelResult {
	if isOverloaded {
		overflow := lambda - mu
		qLen := overflow * math.Max(1, float64(hopCount+1))
		return QueueModelResult{QueueLength: qLen, Delay: qLen / mu}
	}
	rho := lambda / mu
	if rho > 0 {
		ca2 := state.ArrivalCV2
		if ca2 == 0 {
			ca2 = 1.0
		}
		cs2 := state.ServiceCV2
		if cs2 == 0 {
			cs2 = 1.0
		}

		wq := (rho / (1.0 - rho)) * ((ca2 + cs2) / 2.0) * (1.0 / mu)
		delay := wq + (1.0 / mu)
		qLen := lambda * delay
		return QueueModelResult{QueueLength: qLen, Delay: delay}
	}
	return QueueModelResult{QueueLength: 0, Delay: 0}
}

// RegisteredQueueModels defines the active queue models for parallel evaluation.
var RegisteredQueueModels = []QueueModel{
	&MM1QueueModel{},
	&KingmanGG1QueueModel{},
}
