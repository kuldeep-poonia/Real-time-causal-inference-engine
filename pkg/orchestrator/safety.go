package orchestrator

import (
	phase5 "absia/internal/intelligence/phase5_insight"
)

// SafetyReport is the complete safety evaluation produced at the end of every
// pipeline run. It gates the final API response: when FallbackDecision.IsUnknown
// is true, no automated remediation action may be taken.
type SafetyReport struct {
	FusionResult     phase5.FusionResult
	LatentReport     phase5.LatentRiskReport
	ConfidenceReport phase5.ConfidenceReport
	FallbackDecision phase5.FallbackDecision
}

// RunSafetyGate executes the full Phase-5 safety pipeline in sequence:
//
//  1. FuseCausalResults  — classify each cause as root / mediator / rejected confounder.
//  2. AssessLatentRisk   — detect hidden-variable risk via 4 independent evidence channels:
//     correlation-without-path, unexplained residual, ranking instability, coverage collapse.
//  3. ComputeConfidence  — weighted confidence score (CONFIRMED / PROBABLE / UNKNOWN)
//     with latent penalty applied. Safety invariant: LatentRiskHigh unconditionally
//     forces UNKNOWN regardless of numeric score.
//  4. EvaluateFallback   — 5-gate safety decision. Any gate firing sets IsUnknown=true
//     with machine-readable reasons and telemetry probe recommendations.
//
// prevRanking should be nil on the first call. On subsequent calls, pass the
// Explanation.Causes slice from the prior run to detect ranking instability across runs.
//
// The full pipeline MUST NOT surface a final response to the API without first
// calling this function and inspecting FallbackDecision.IsUnknown.
func RunSafetyGate(
	graph *phase5.CausalGraph,
	exp phase5.Explanation,
	prevRanking []string,
	target string,
) SafetyReport {
	// Step 1: Classify causal roles — root causes, mediators, rejected confounders.
	// FuseCausalResults resolves structural conflicts from Phase 4's explanation
	// against the Phase 5 graph topology.
	fusion := phase5.FuseCausalResults(graph, "", exp.Causes, target)

	// Step 2: Assess latent (hidden variable) risk.
	// Four orthogonal channels: correlation-without-path (HIGH severity),
	// unexplained residual (MEDIUM), ranking instability (HIGH), coverage collapse (MEDIUM).
	latent := phase5.AssessLatentRisk(graph, exp, prevRanking, target)

	// Step 3: Compute calibrated confidence score with latent penalty.
	// Score = weighted(GraphCoverage, Determinism, ResidualExplained, RoleConsistency) − LatentPenalty
	confidence := phase5.ComputeConfidence(fusion, graph, exp, latent)

	// Step 4: Evaluate five-gate safety decision.
	// Gates: LatentHighRisk, LowConfidence, GraphMismatch, SevereCoverage, RankingInstability.
	// All gates evaluated (no short-circuit) so full reason list is available for operators.
	fallback := phase5.EvaluateFallback(confidence, latent, fusion, graph, target)

	return SafetyReport{
		FusionResult:     fusion,
		LatentReport:     latent,
		ConfidenceReport: confidence,
		FallbackDecision: fallback,
	}
}
