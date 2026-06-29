package phase3_causal

import (
	"time"
)

/*
PHASE 3 → TYPES (EXTENDED)

NodeState added to Node for Phase 3 to consume Phase 1 physics variables.
All existing types unchanged.
*/

// NodeState — physics state variables from M/M/1 model.
// Populated by Phase 1 GetNodeState() and attached to Node before propagation.
type NodeState struct {
	Load            float64 // ρ = λ/μ
	ArrivalRate     float64 // λ
	ServiceRate     float64 // μ
	QueueLength     float64 // L (M/M/1)
	ProcessingDelay float64 // W (M/M/1)
	Timestamp       float64
	DominantSignal  string

	// Kingman Burst-Aware Variables
	ArrivalCV2             float64 // C_A^2
	ServiceCV2             float64 // C_S^2
	QueueLengthKingman     float64 // L (G/G/1)
	ProcessingDelayKingman float64 // W (G/G/1)
}

/*
NODE — extended with State
*/
type Node struct {
	ID   string
	Name string
	Series []float64

	Type       string
	Observable bool

	Intervenable bool

	InferenceConfidence float64

	// NEW: physics state (set from Phase 1 NodeState)
	State NodeState
}

/*
EDGE TYPE
*/
type EdgeType int

const (
	Direct EdgeType = iota
	Confounder
	Mediator
)

/*
EDGE SOURCE
*/
type EdgeSource string

const (
	EdgeSourceObserved EdgeSource = "Observed"
	EdgeSourceInferred EdgeSource = "Inferred"
)

/*
INTERVENTION EFFECT
*/
type InterventionEffect struct {
	TargetValue float64
	Effect      float64
}

/*
EDGE
*/
type Edge struct {
	From string
	To   string

	ExistenceProb  float64
	CausalStrength float64
	SourceSeries []float64
	TargetSeries []float64

	DoEffects []InterventionEffect

	Type EdgeType

	Identifiable bool

	Conditions map[string]float64

	Mean     float64
	Variance float64

	// Phase 4 Epistemic Fields
	Source        EdgeSource
	Uncertainty   float64
	EvidenceBasis string
	ExpiresAt     time.Time
}

/*
MULTI-CAUSE FACTOR
*/
type CausalFactor struct {
	NodeIDs []string
	Target  string

	InteractionType string

	Strength float64

	Identifiable bool

	Conditions map[string]float64

	Mean     float64
	Variance float64
}

/*
EVENT
*/
type Event struct {
	NodeID    string
	Value     float64
	Timestamp time.Time

	Type       string
	Intensity  float64
	Confidence float64

	NoiseLevel float64
	Baseline   float64
}

/*
TEMPORAL NODE
*/
type TemporalNode struct {
	NodeID string
	Time   time.Time

	Value      float64
	Noise      float64
	Intensity  float64
	Confidence float64
}

/*
TEMPORAL POINT
*/
type TemporalPoint struct {
	Time time.Time
	Node *TemporalNode
}

/*
TEMPORAL SERIES
*/
type TemporalSeries struct {
	Points  []TemporalPoint
	Density float64
}

/*
TEMPORAL EDGE
*/
type TemporalEdge struct {
	From TemporalNode
	To   TemporalNode

	Lag time.Duration

	ExistenceProb  float64
	CausalStrength float64

	IsFeedbackLoop bool

	Identifiable bool

	Conditions map[string]float64

	Mean     float64
	Variance float64
	Discontinuous bool
}

/*
GRAPH
*/
type Graph struct {
	Nodes map[string]*Node
	Edges []*Edge

	Factors []*CausalFactor

	IsDynamic bool

	LogLikelihood float64
	PosteriorProb float64
}

/*
TEMPORAL GRAPH
*/
type TemporalGraph struct {
	Nodes map[string]*TemporalSeries
	Edges []*TemporalEdge
}

/*
CAUSAL HYPOTHESIS
*/
type CausalHypothesis struct {
	ID string
	Target string

	Subgraph *Graph

	Probability float64

	Mean     float64
	Variance float64

	Description string
}

/*
SYSTEM STATE
*/
type SystemState struct {
	Load            float64
	ArrivalRate     float64
	ServiceRate     float64
	QueueLength     float64
	ProcessingDelay float64
	Timestamp       float64
}

/*
WEIGHTED GRAPH
*/
type WeightedGraph struct {
	Graph       *Graph
	Probability float64

	ArrivalRate     float64
ServiceRate     float64
QueueLength     float64
ProcessingDelay float64
}

/*
GRAPH DISTRIBUTION
*/
type GraphDistribution struct {
	Items []WeightedGraph

	Normalized bool
}
