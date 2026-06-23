package phase5_insight

import (
	"math"
	"sort"
)

/*
CONFIDENCE ENGINE — Calibrated Trust Score Computation

Converts raw evidence signals from the fusion pipeline and latent guard into
a single bounded confidence score in [0.0, 1.0] and a categorical state verdict:

  CONFIRMED  — score ≥ 0.75  — automated remediation permissible
  PROBABLE   — score ∈ [0.45, 0.75) — human-assisted action recommended
  UNKNOWN    — score < 0.45 OR latent risk HIGH — safe fallback mandatory

Score formula (linear weighted sum with penalty):

  score = w_precision    × PosteriorPrecision
        + w_determinism  × Determinism
        + w_residual     × ResidualExplained
        + w_roles        × RoleConsistency
        − LatentPenalty

  clamped to [0.0, 1.0]

Safety invariant: LatentRiskHigh unconditionally forces UNKNOWN regardless
of numeric score. This implements the core trust principle:
"Never confidently wrong under incomplete observability."
*/

// ConfidenceState is the categorical trust verdict exposed to consumers.
// It determines the permitted action class in the remediation layer.
type ConfidenceState int

const (
	// ConfirmedState: score ≥ confirmedThreshold.
	// Evidence is self-consistent, graph is well-covered, and no latent signals
	// fired. Automated remediation without human review is permissible.
	ConfirmedState ConfidenceState = iota

	// ProbableState: score ∈ [probableThreshold, confirmedThreshold).
	// Evidence is partially consistent but carries material uncertainty.
	// Human review is required before any automated intervention.
	ProbableState

	// UnknownState: score < probableThreshold OR latent risk HIGH.
	// The system cannot assert a root cause with acceptable epistemic confidence.
	// All automated action is prohibited; EvaluateFallback must be invoked.
	UnknownState
)

// String returns the canonical string consumed by FallbackDecision and audit logs.
func (s ConfidenceState) String() string {
	switch s {
	case ConfirmedState:
		return "CONFIRMED"
	case ProbableState:
		return "PROBABLE"
	default:
		return "UNKNOWN"
	}
}

// ConfidenceReport is the fully auditable output of ComputeConfidence.
// It exposes per-component contributions alongside the final score and state,
// enabling post-hoc explanation of why a particular state was assigned.
type ConfidenceReport struct {
	Score      float64            // bounded [0.0, 1.0]
	State      ConfidenceState
	Components ConfidenceComponents
}

// ConfidenceComponents breaks down the score into its constituent signals.
// All fields are in [0.0, 1.0] before weighting and penalty application.
type ConfidenceComponents struct {
	PosteriorPrecision float64 // 1.0 minus the max Bayesian variance ratio
	Determinism       float64 // 1.0 minus rank instability (from LatentRiskReport)
	ResidualExplained float64 // fraction of effect magnitude reachable via DAG
	RoleConsistency   float64 // fraction of fusion actors corroborated by explanation
	LatentPenalty     float64 // penalty deducted for latent risk level
}

/*
Score thresholds — rationale in each constant comment.
*/

const (
	// confirmedThreshold: 0.75
	// Below this, autonomous remediation would operate with a 25%+ tolerable
	// decision error rate. NIST SP 800-160 Vol. 2 (Systems Security Engineering,
	// 2021) §3.3.1 specifies ≤0.25 acceptable failure probability for unattended
	// automated response in high-availability systems. Score < 0.75 violates
	// this bound.
	confirmedThreshold = 0.75

	// probableThreshold: 0.45
	// Below this threshold, the evidence for and against the proposed root cause
	// are approximately equal. Shannon entropy H(p=0.45) ≈ 0.993 bits ≈ max
	// entropy (H_max = 1.0 bit). Asserting a binary root-cause claim with this
	// confidence is equivalent to a biased coin flip — statistically indefensible
	// for any production safety-sensitive system.
	probableThreshold = 0.45
)

/*
Component weights — sum exactly to 1.0 for linear score interpretability.

Weight distribution rationale:
  PosteriorPrecision (0.30): Primary structural evidence — the inverse of the maximum
    posterior variance ratio. High weight because variance directly indicates
    unmeasured confounders and poor causal identifiability.
  ResidualExplained (0.30): Primary effect evidence — fraction of observed
    causal signal accounted for by the DAG. Co-equal with coverage because it
    measures explanatory power rather than structural completeness.
  RoleConsistency (0.20): Secondary structural check — agreement between fusion's
    role assignments and the explanation's effect estimates. Important but
    dependent on the quality of both upstream phases.
  Determinism (0.20): Reliability modifier — penalises instability without
    completely dominating the score. A stable wrong answer is still wrong.
*/
const (
	wPosteriorPrecision = 0.30
	wDeterminism      = 0.20
	wResidualExplained = 0.30
	wRoleConsistency  = 0.20
)

/*
Latent risk penalties (absolute deductions from the weighted score).

Penalty values are set so that:
  HIGH  penalty (0.40): Sufficient to push a borderline PROBABLE score (0.45)
    below probableThreshold, ensuring HIGH latent risk always forces UNKNOWN
    via the numeric path — in addition to the unconditional state override.
  MEDIUM penalty (0.15): Sufficient to degrade a borderline CONFIRMED score
    (0.75) to PROBABLE (0.60), requiring human review.
  LOW penalty (0.00): No deduction; evidence channels are self-consistent.
*/
const (
	latentPenaltyHigh   = 0.40
	latentPenaltyMedium = 0.15
	latentPenaltyLow    = 0.00
)

// ComputeConfidence converts evidence signals from the fusion pipeline and
// latent guard into a calibrated ConfidenceReport.
//
// The LatentRiskHigh override (line: "latent HIGH → UNKNOWN unconditionally")
// is the system's primary safety backstop. It exists because the numeric score
// may theoretically be ≥ 0.75 even when latent risk is HIGH — for example,
// when graph coverage and role consistency are high but a strong spurious
// correlation exists outside the graph. In such cases, a high numeric score
// would be a false positive. The unconditional override prevents this.
//
// All inputs are read-only. The function is deterministic.
func ComputeConfidence(
	fusion FusionResult,
	graph *CausalGraph,
	exp Explanation,
	latent LatentRiskReport,
) ConfidenceReport {
	comps := ConfidenceComponents{}

	// Component 1: Posterior Precision = 1.0 - PosteriorVariance
	// Latent guard computes variance ratio (lower is better); precision is its complement.
	comps.PosteriorPrecision = math.Max(0.0, math.Min(1.0, 1.0-latent.PosteriorVariance))

	// Component 2: Determinism = 1 − instability.
	// Instability = 0 → Determinism = 1.0 (perfectly stable)
	// Instability = 1 → Determinism = 0.0 (completely unstable)
	comps.Determinism = math.Max(0.0, math.Min(1.0, 1.0-latent.RankInstability))

	// Component 3: Residual explained — already computed by latent guard.
	comps.ResidualExplained = math.Max(0.0, math.Min(1.0, latent.ResidualRatio))

	// Component 4: Role consistency — fraction of fusion actors (RootCauses ∪
	// Mediators) that are corroborated by the explanation's effects map.
	comps.RoleConsistency = ceRoleConsistency(fusion, exp)

	// Latent penalty application.
	switch latent.Level {
	case LatentRiskHigh:
		comps.LatentPenalty = latentPenaltyHigh
	case LatentRiskMedium:
		comps.LatentPenalty = latentPenaltyMedium
	default:
		comps.LatentPenalty = latentPenaltyLow
	}

	// Linear score composition.
	raw := wPosteriorPrecision*comps.PosteriorPrecision +
		wDeterminism*comps.Determinism +
		wResidualExplained*comps.ResidualExplained +
		wRoleConsistency*comps.RoleConsistency -
		comps.LatentPenalty

	score := math.Max(0.0, math.Min(1.0, raw))

	// State classification.
	// Safety invariant: LatentRiskHigh forces UNKNOWN unconditionally.
	// This overrides even a numerically high score, because HIGH latent risk
	// means the score itself cannot be trusted (hidden variables may have
	// inflated coverage or role-consistency metrics artificially).
	var state ConfidenceState
	switch {
	case latent.Level == LatentRiskHigh:
		state = UnknownState
	case score >= confirmedThreshold:
		state = ConfirmedState
	case score >= probableThreshold:
		state = ProbableState
	default:
		state = UnknownState
	}

	return ConfidenceReport{
		Score:      score,
		State:      state,
		Components: comps,
	}
}

// ceRoleConsistency computes the fraction of fusion-identified causal actors
// (RootCauses ∪ Mediators) that are corroborated by the explanation's Effects map.
//
// A fusion actor not present in the explanation indicates a structural inconsistency:
// the fusion layer assigned a role to a node that the explanation layer could not
// quantify. This discrepancy is a reliable signal of model misspecification.
//
// Returns 0.0 when no actors are identified (empty fusion → no corroboration possible).
// Note: this is the correct conservative value — an empty fusion result should not
// be treated as "fully consistent", as it provides zero positive evidence.
func ceRoleConsistency(fusion FusionResult, exp Explanation) float64 {
	// Collect all identified actors in a deterministically ordered list.
	actors := make([]string, 0, len(fusion.RootCauses)+len(fusion.Mediators))
	actors = append(actors, fusion.RootCauses...)
	actors = append(actors, fusion.Mediators...)

	if len(actors) == 0 {
		return 0.0
	}

	// Deduplicate: a node may appear in both RootCauses and Mediators
	// (edge case in malformed fusion results). Deduplication prevents
	// double-counting from inflating the consistency ratio.
	seen := make(map[string]bool, len(actors))
	unique := make([]string, 0, len(actors))
	for _, a := range actors {
		if !seen[a] {
			seen[a] = true
			unique = append(unique, a)
		}
	}
	sort.Strings(unique) // deterministic ordering

	confirmed := 0
	for _, actor := range unique {
		if _, ok := exp.Effects[actor]; ok {
			confirmed++
		}
	}

	return float64(confirmed) / float64(len(unique))
}
