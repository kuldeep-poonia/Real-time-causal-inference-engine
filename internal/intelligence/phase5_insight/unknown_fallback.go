package phase5_insight

import "sort"

/*
UNKNOWN FALLBACK — Final Safety Gate

This is the last line of defense before a root-cause assertion is surfaced
to any consumer (API, operator UI, auto-remediation engine).

When called after ComputeConfidence and AssessLatentRisk, it evaluates five
independent gate conditions. If any gate fires, the system returns UNKNOWN
with a machine-readable reason code, a mandatory human-review policy, and
actionable telemetry probe recommendations.

Core invariant:
  "Unknown is preferred over unsafe confidence."

This invariant overrides all other product goals including response latency,
root-cause certainty, and remediation aggressiveness.

Usage in Phase 5 fusion flow:
  latent   := AssessLatentRisk(graph, exp, prevRanking, target)
  conf     := ComputeConfidence(fusion, graph, exp, latent)
  fallback := EvaluateFallback(conf, latent, fusion, graph, target)
  if fallback.IsUnknown {
      // surface FallbackDecision to operator; do NOT proceed with remediation
  }
*/

// FallbackReason is a machine-readable enumeration of why UNKNOWN was triggered.
// Each value corresponds to exactly one gate in EvaluateFallback.
// Multiple reasons may be active simultaneously (stored in a slice, not a bitmask,
// to preserve human-readable ordering and avoid positional coupling to LatentSignal).
type FallbackReason int

const (
	FallbackReasonNone               FallbackReason = 0
	FallbackReasonLatentHighRisk     FallbackReason = 1 // latent.Level == LatentRiskHigh
	FallbackReasonLowConfidence      FallbackReason = 2 // confidence.Score < probableThreshold
	FallbackReasonGraphMismatch      FallbackReason = 3 // fusion root not present in graph
	FallbackReasonSevereVariance     FallbackReason = 4 // variance > fallbackSevereVarianceThreshold
	FallbackReasonRankingInstability FallbackReason = 5 // SignalRankingInstability in latent
)

// String returns the canonical string for audit logs and operator alerts.
func (r FallbackReason) String() string {
	switch r {
	case FallbackReasonLatentHighRisk:
		return "LATENT_HIGH_RISK"
	case FallbackReasonLowConfidence:
		return "LOW_CONFIDENCE"
	case FallbackReasonGraphMismatch:
		return "GRAPH_MISMATCH"
	case FallbackReasonSevereVariance:
		return "SEVERE_POSTERIOR_VARIANCE"
	case FallbackReasonRankingInstability:
		return "RANKING_INSTABILITY"
	default:
		return "NONE"
	}
}

// RemediationPolicy specifies the mandatory human-control posture when UNKNOWN fires.
// Only one policy is defined in this system: all fallback cases require human review.
// No automated remediation may proceed when EvaluateFallback returns IsUnknown=true.
type RemediationPolicy int

const (
	// PolicyHumanReview is the only valid policy for UNKNOWN decisions.
	// It is intentionally the zero value so that zero-initialised structs
	// default to the safest policy rather than an undefined one.
	PolicyHumanReview RemediationPolicy = 0
)

// String returns the canonical policy label.
func (p RemediationPolicy) String() string {
	return "HUMAN_REVIEW_REQUIRED"
}

// TelemetryProbe is an actionable recommendation for additional data collection
// that could resolve the uncertainty causing the UNKNOWN decision.
// Each probe targets a specific node with a specific measurement strategy.
type TelemetryProbe struct {
	NodeID    string // target node for instrumentation
	Metric    string // suggested measurement or trace type
	Rationale string // human-readable explanation of why this probe resolves uncertainty
}

// FallbackDecision is the complete safety output of EvaluateFallback.
// When IsUnknown is true, no automated action may be taken.
// ProbeRecommendations provide a concrete path toward resolving the uncertainty.
type FallbackDecision struct {
	IsUnknown            bool
	Reasons              []FallbackReason
	RemediationPolicy    RemediationPolicy
	ProbeRecommendations []TelemetryProbe
	ConfidenceScore      float64         // snapshot of score at decision time
	LatentLevel          LatentRiskLevel // snapshot of latent level at decision time
}

/*
Fallback-specific thresholds (additional to those in confidence_engine.go).
*/

const (
	// fallbackSevereVarianceThreshold: 0.40
	// A more conservative ceiling than latentVarianceSNRThreshold (0.25).
	// At >= 0.40 variance ratio, the causal effect is deeply buried in noise.
	// This represents a severe structural mismatch between the inferred model
	// and the observable system. Used as an independent fallback gate.
	fallbackSevereVarianceThreshold = 0.40
)

// EvaluateFallback is the final safety gate in the Phase 5 fusion pipeline.
//
// It must be called AFTER both AssessLatentRisk and ComputeConfidence.
// It evaluates five independent gate conditions in order of severity:
//
//  Gate 1 — Latent HIGH risk (unconditional: no model should assert under HIGH)
//  Gate 2 — Confidence below safe operating threshold
//  Gate 3 — Graph mismatch (fusion root names nodes absent from graph)
//  Gate 4 — Severe posterior variance (independent of latent guard's variance check)
//  Gate 5 — Ranking instability was a direct latent trigger
//
// If any gate fires, IsUnknown is true and ProbeRecommendations are generated.
// All gates are evaluated regardless of whether a prior gate fired (no short-circuit),
// because the full reasons list is required for operator diagnosis.
//
// The function is deterministic and read-only.
func EvaluateFallback(
	confidence ConfidenceReport,
	latent LatentRiskReport,
	fusion FusionResult,
	graph *CausalGraph,
	target string,
) FallbackDecision {
	reasons := make([]FallbackReason, 0, 5)

	// Gate 1: Latent HIGH risk.
	// No amount of numeric confidence can compensate for a suspected hidden
	// confounder. The system simply does not know what it does not observe.
	if latent.Level == LatentRiskHigh {
		reasons = append(reasons, FallbackReasonLatentHighRisk)
	}

	// Gate 2: Confidence score below safe operating threshold.
	// probableThreshold (0.45) is imported from confidence_engine.go constants.
	// Using the same threshold here ensures consistency between the state
	// classification in ComputeConfidence and the fallback gate here.
	if confidence.Score < probableThreshold {
		reasons = append(reasons, FallbackReasonLowConfidence)
	}

	// Gate 3: Graph mismatch.
	// If fusion assigned a root cause to a node that does not exist in the
	// current graph, the fusion output was computed against a different (stale
	// or hypothetical) model. Acting on this output would be epistemically invalid.
	if fbHasGraphMismatch(fusion, graph) {
		reasons = append(reasons, FallbackReasonGraphMismatch)
	}

	// Gate 4: Severe posterior variance.
	// This gate fires at a higher threshold (0.40) than the latent guard's
	// variance check (0.25), making it an independent backstop for extreme cases
	// where model structure is fundamentally wrong.
	if latent.PosteriorVariance > fallbackSevereVarianceThreshold {
		reasons = append(reasons, FallbackReasonSevereVariance)
	}

	// Gate 5: Ranking instability triggered directly in the latent report.
	// Even if the numeric confidence score is acceptable, repeated instability
	// in cause ranking indicates the system would recommend different root
	// causes across identical evidence — which is unacceptable for any
	// remediation system that requires reproducibility.
	if latent.Signals&SignalRankingInstability != 0 {
		reasons = append(reasons, FallbackReasonRankingInstability)
	}

	isUnknown := len(reasons) > 0

	var probes []TelemetryProbe
	if isUnknown {
		probes = fbBuildProbes(latent, fusion, graph, target)
	}

	return FallbackDecision{
		IsUnknown:            isUnknown,
		Reasons:              reasons,
		RemediationPolicy:    PolicyHumanReview,
		ProbeRecommendations: probes,
		ConfidenceScore:      confidence.Score,
		LatentLevel:          latent.Level,
	}
}

// fbHasGraphMismatch returns true if any fusion RootCause names a node
// that does not exist in the graph's node map.
//
// A nil graph with non-empty RootCauses is always a mismatch: the fusion result
// was computed against some model, but the current graph provides no structural
// support for it.
func fbHasGraphMismatch(fusion FusionResult, graph *CausalGraph) bool {
	if graph == nil {
		return len(fusion.RootCauses) > 0
	}
	for _, root := range fusion.RootCauses {
		if _, ok := graph.Nodes[root]; !ok {
			return true
		}
	}
	return false
}

// fbBuildProbes constructs a deduplicated, deterministically ordered list of
// telemetry probes tailored to the specific signals that triggered UNKNOWN.
//
// Three probe classes are generated:
//
//  Class A — Suspicious nodes from correlation-without-path signal:
//    These nodes have measurable effects on the target but no graph path.
//    A causal path trace may reveal hidden mediating nodes.
//
//  Class B — Rejected nodes from fusion:
//    Nodes rejected as confounders may be latent common causes. Isolating
//    their independent effect confirms or refutes this hypothesis.
//
//  Class C — Target node for residual decomposition:
//    When unexplained residual is high, variance decomposition on the target
//    can locate unobserved inputs by their contribution to target variance.
func fbBuildProbes(
	latent LatentRiskReport,
	fusion FusionResult,
	graph *CausalGraph,
	target string,
) []TelemetryProbe {
	probes := make([]TelemetryProbe, 0)
	seen := make(map[string]bool)

	// Class A: Suspicious nodes from correlation-without-path signal.
	// Sort for deterministic probe ordering.
	suspiciousSorted := make([]string, len(latent.SuspiciousNodes))
	copy(suspiciousSorted, latent.SuspiciousNodes)
	sort.Strings(suspiciousSorted)

	for _, nodeID := range suspiciousSorted {
		if seen[nodeID] {
			continue
		}
		seen[nodeID] = true
		probes = append(probes, TelemetryProbe{
			NodeID: nodeID,
			Metric: "causal_path_trace",
			Rationale: "Node carries significant effect on target but has no directed" +
				" path in the current DAG. Tracing intermediate nodes may expose" +
				" a latent mediator or a missing graph edge.",
		})
	}

	// Class B: Fusion-rejected nodes (potential unmodelled confounders).
	rejectedSorted := make([]string, len(fusion.Rejected))
	copy(rejectedSorted, fusion.Rejected)
	sort.Strings(rejectedSorted)

	for _, nodeID := range rejectedSorted {
		if seen[nodeID] {
			continue
		}
		seen[nodeID] = true
		probes = append(probes, TelemetryProbe{
			NodeID: nodeID,
			Metric: "confounder_isolation_trace",
			Rationale: "Fusion rejected this node as a confounder candidate. Isolating" +
				" its direct effect on the target (independent of other causes)" +
				" confirms or refutes whether it is a latent common cause.",
		})
	}

	// Class C: Target node for residual variance decomposition.
	// Appended last; always generated when probes are being built.
	if !seen[target] {
		probes = append(probes, TelemetryProbe{
			NodeID: target,
			Metric: "residual_variance_decomposition",
			Rationale: "Decompose target node variance across all candidate inputs to" +
				" quantify the unexplained fraction attributable to unobserved nodes." +
				" Prioritise inputs with zero current coverage in the causal graph.",
		})
	}

	return probes
}
