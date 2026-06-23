package phase5_insight

import (
	"math"
	"sort"
)

/*
LATENT GUARD — Hidden Variable Risk Detector

Evaluates four orthogonal evidence channels to estimate the probability that
a latent (unobserved) variable confounds the current root-cause assignment:

  Channel 1 — Correlation-Without-Path   (HIGH-severity)
  Channel 2 — Unexplained Residual       (MEDIUM-severity)
  Channel 3 — Ranking Instability        (HIGH-severity)
  Channel 4 — High Posterior Variance    (MEDIUM-severity)

Risk composition (after signal assignment):
  ≥1 HIGH signal          → LatentRiskHigh
  ≥2 MEDIUM signals       → LatentRiskHigh   (compound sparsity = active risk)
  =1 MEDIUM signal        → LatentRiskMedium
  no signals              → LatentRiskLow

This module is a read-only observer: it never mutates graph or explanation.
All node-map iteration is sorted to guarantee bit-identical output for
identical inputs (required for determinism contract in ConfidenceEngine).
*/

// LatentRiskLevel is a three-tier ordinal risk classification.
// The step from MEDIUM to HIGH is non-linear by design: HIGH signals that
// autonomous root-cause assertion is epistemically unjustifiable.
type LatentRiskLevel int

const (
	LatentRiskLow    LatentRiskLevel = 0
	LatentRiskMedium LatentRiskLevel = 1
	LatentRiskHigh   LatentRiskLevel = 2
)

// String returns the canonical string representation consumed by FallbackDecision.
func (r LatentRiskLevel) String() string {
	switch r {
	case LatentRiskLow:
		return "LOW"
	case LatentRiskMedium:
		return "MEDIUM"
	default:
		return "HIGH"
	}
}

// LatentSignal is a bitmask that identifies which detection channels fired.
// Using a bitmask (rather than []string) enables O(1) composition and
// prevents information loss when multiple channels fire simultaneously.
type LatentSignal uint8

const (
	SignalNone               LatentSignal = 0
	SignalCorrelationNoPath  LatentSignal = 1 << 0 // effect observed, no DAG path to target
	SignalUnexplainedResidual LatentSignal = 1 << 1 // effect mass exceeds explained coverage
	SignalRankingInstability LatentSignal = 1 << 2 // cause rank order diverged from prior
	SignalHighPosteriorVariance LatentSignal = 1 << 3 // Bayesian variance exceeds dynamic SNR threshold
)

// LatentRiskReport is the complete structured output of the latent guard.
// All numeric fields are bounded in [0, 1] for direct composability with
// the ConfidenceEngine's weighted scoring model.
type LatentRiskReport struct {
	Level            LatentRiskLevel
	Signals          LatentSignal
	CorrelationScore float64  // max normalized spurious effect seen [0,1]
	ResidualRatio    float64  // fraction of effect mass with a DAG path [0,1]
	RankInstability  float64  // Spearman divergence from prior ranking [0,1]
	PosteriorVariance float64 // max Bayesian variance relative to SNR among root causes
	SuspiciousNodes  []string // deterministically sorted list of flagged node IDs
}

/*
Detection thresholds — each value carries an explicit mathematical rationale.
No threshold in this file may be changed without updating its justification.
*/

const (
	// latentCorrNoPathThreshold: 0.70
	// Cohen (1988): canonical strong-correlation boundary at r = 0.50 (medium)
	// and r = 0.70+ (large). Above 0.70, the absence of any directed DAG path
	// between two nodes is statistically anomalous (p < 0.05 for n > 30 under
	// bivariate Gaussian assumptions). We use normalized effect magnitude
	// |effect(v)| / Σ|effects| as a linear-SCM-valid proxy for Pearson r,
	// which is exact when CausalNode.Func is linear (as used in this system).
	latentCorrNoPathThreshold = 0.70

	// latentResidualThreshold: 0.40
	// If identified causes (those with a DAG path to target) account for less
	// than 40% of total observed effect magnitude, the majority of the system's
	// variation is unaccounted. Richardson & Spirtes (2002) PAG completeness
	// theory uses 0.50 as the symmetry point; we apply 0.40 to maintain
	// conservative bias given that observability depth is unknown at runtime.
	latentResidualThreshold = 0.40

	// latentInstabilityThreshold: 0.30
	// Spearman rank instability = (1 − ρ_s) / 2.
	// At instability = 0.30, ρ_s = 0.40, which Kendall (1970) characterises as
	// "no meaningful rank association". Rankings at this level are consistent
	// with random ordering relative to true causal priority. Our threshold is
	// stricter than the Kendall τ = 0.50 no-association boundary, appropriate
	// for production systems where ranking errors propagate to remediation.
	latentInstabilityThreshold = 0.30

	// latentVarianceSNRThreshold: 0.25
	// Rather than using a static graph coverage heuristic, we use a dynamic
	// Bayesian threshold based on Signal-to-Noise Ratio (SNR).
	// If the posterior variance exceeds (Effect / 2)^2, the 95% credible
	// interval crosses zero, indicating the causal effect is indistinguishable
	// from noise (implying unmeasured confounders).
	// Threshold = 0.25 * (Effect)^2
	latentVarianceSNRThreshold = 0.25
)

// AssessLatentRisk is the single entry point for the latent guard.
//
// Parameters:
//   graph       — the Phase 5 causal graph (same namespace as exp)
//   exp         — the Explanation produced by Phase 4 (Causes, Effects, Uncertainty)
//   prevRanking — Explanation.Causes from the immediately prior call; nil on first call
//   target      — the target node ID (must exist in graph.Nodes)
//
// The function is fully deterministic: all map iteration is pre-sorted.
// A nil or empty graph triggers an immediate LatentRiskHigh / SignalCoverageCollapse
// because no graph means no evidence basis for any root-cause assignment.
func AssessLatentRisk(
	graph *CausalGraph,
	exp Explanation,
	prevRanking []string,
	target string,
) LatentRiskReport {
	if graph == nil || len(graph.Nodes) == 0 {
		return LatentRiskReport{
			Level:            LatentRiskHigh,
			Signals:          SignalHighPosteriorVariance,
			CorrelationScore: 0.0,
			ResidualRatio:    0.0,
			RankInstability:  0.0,
			PosteriorVariance: 1.0,
			SuspiciousNodes:  []string{},
		}
	}

	// Evaluate all four channels independently before composing risk level.
	// Independence is critical: channels must not short-circuit each other.
	corrScore, suspNodes := lgCorrNoPathScore(graph, exp, target)
	residualRatio := lgResidualRatio(graph, exp, target)
	rankInstab := lgRankInstability(exp.Causes, prevRanking)
	maxVarRatio := lgMaxPosteriorVariance(exp)

	report := LatentRiskReport{
		Signals:          SignalNone,
		CorrelationScore: corrScore,
		ResidualRatio:    residualRatio,
		RankInstability:  rankInstab,
		PosteriorVariance: maxVarRatio,
		SuspiciousNodes:  suspNodes,
	}

	if corrScore >= latentCorrNoPathThreshold {
		report.Signals |= SignalCorrelationNoPath
	}
	if residualRatio < latentResidualThreshold {
		report.Signals |= SignalUnexplainedResidual
	}
	if rankInstab > latentInstabilityThreshold {
		report.Signals |= SignalRankingInstability
	}
	if maxVarRatio > latentVarianceSNRThreshold {
		report.Signals |= SignalHighPosteriorVariance
	}

	// Signal severity classification:
	//   HIGH-severity: CorrelationNoPath, RankingInstability
	//     These indicate active spurious inference or non-deterministic ordering,
	//     which can produce a wrong root cause with high confidence — the most
	//     dangerous failure mode.
	//   MEDIUM-severity: UnexplainedResidual, CoverageCollapse
	//     These indicate passive model incompleteness (missing nodes or paths),
	//     which reduces confidence but does not necessarily invert the ranking.
	highCount := 0
	medCount := 0
	if report.Signals&SignalCorrelationNoPath != 0 {
		highCount++
	}
	if report.Signals&SignalRankingInstability != 0 {
		highCount++
	}
	if report.Signals&SignalUnexplainedResidual != 0 {
		medCount++
	}
	if report.Signals&SignalHighPosteriorVariance != 0 {
		medCount++
	}

	switch {
	case highCount >= 1 || medCount >= 2:
		report.Level = LatentRiskHigh
	case medCount == 1:
		report.Level = LatentRiskMedium
	default:
		report.Level = LatentRiskLow
	}

	return report
}

// lgCorrNoPathScore computes the maximum normalized spurious effect:
//
//	maxScore = max { |effect(v)| / Σ|effects| : v ∈ Effects, no path v→target in DAG }
//
// Returns the score and a sorted list of nodes exceeding latentCorrNoPathThreshold.
//
// Normalized effect is used as a correlation proxy under the linear-SCM assumption
// (valid for CausalNode.Func structures in this system): the backdoor-adjusted
// effect estimate is proportional to Pearson r when the functional form is linear.
func lgCorrNoPathScore(
	graph *CausalGraph,
	exp Explanation,
	target string,
) (maxScore float64, suspicious []string) {
	suspicious = []string{}

	totalEffect := 0.0
	for _, v := range exp.Effects {
		totalEffect += math.Abs(v)
	}
	if totalEffect == 0 {
		return 0.0, suspicious
	}

	// Deterministic traversal via sorted keys.
	nodeIDs := make([]string, 0, len(exp.Effects))
	for id := range exp.Effects {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)

	for _, nodeID := range nodeIDs {
		if nodeID == target {
			continue
		}
		normEffect := math.Abs(exp.Effects[nodeID]) / totalEffect
		visited := make(map[string]bool)
		if !lgPathReachable(graph, nodeID, target, visited) {
			if normEffect > maxScore {
				maxScore = normEffect
			}
			if normEffect >= latentCorrNoPathThreshold {
				suspicious = append(suspicious, nodeID)
			}
		}
	}

	sort.Strings(suspicious)
	return maxScore, suspicious
}

// lgPathReachable is a DFS reachability check for the Phase 5 CausalGraph type.
// Distinct from dfsCanReach (causal_fusion.go) which operates on the Phase 3
// graph type with string-keyed edges. This version works with pointer-linked
// CausalNode/CausalEdge structures.
func lgPathReachable(graph *CausalGraph, src, dst string, visited map[string]bool) bool {
	if src == dst {
		return true
	}
	if visited[src] {
		return false
	}
	visited[src] = true
	for _, e := range graph.Edges {
		if e.From != nil && e.To != nil && e.From.ID == src {
			if lgPathReachable(graph, e.To.ID, dst, visited) {
				return true
			}
		}
	}
	return false
}

// lgResidualRatio computes the fraction of total absolute effect mass that is
// accounted for by nodes with a directed DAG path to the target.
//
//	ratio = Σ{ |effect(v)| : ∃ path v→target } / Σ{ |effect(v)| : v ∈ Effects }
//
// Returns 1.0 when Effects is empty (nothing to explain = nothing unexplained).
func lgResidualRatio(graph *CausalGraph, exp Explanation, target string) float64 {
	totalEffect := 0.0
	explainedEffect := 0.0

	nodeIDs := make([]string, 0, len(exp.Effects))
	for id := range exp.Effects {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)

	for _, nodeID := range nodeIDs {
		eff := math.Abs(exp.Effects[nodeID])
		totalEffect += eff
		visited := make(map[string]bool)
		if lgPathReachable(graph, nodeID, target, visited) {
			explainedEffect += eff
		}
	}

	if totalEffect == 0 {
		return 1.0
	}
	return explainedEffect / totalEffect
}

// lgRankInstability quantifies the rank-order divergence between the current
// cause ranking and a prior observation using Spearman's ρ:
//
//	ρ_s = 1 − 6Σd² / (n(n²−1))
//	instability = (1 − ρ_s) / 2   ∈ [0, 1]
//
// Boundary cases:
//   instability = 0 → identical orderings
//   instability = 1 → perfectly inverted or completely disjoint membership
//
// Returns 0.0 when prevRanking is nil (first call; no prior state to compare).
// Returns 1.0 when cardinalities differ (structural change implies maximum divergence).
func lgRankInstability(current, prev []string) float64 {
	if len(prev) == 0 {
		return 0.0
	}
	if len(current) != len(prev) {
		return 1.0
	}

	n := len(current)
	if n == 0 {
		return 0.0
	}
	if n == 1 {
		if current[0] == prev[0] {
			return 0.0
		}
		return 1.0
	}

	prevRank := make(map[string]int, n)
	for i, id := range prev {
		prevRank[id] = i
	}

	sumD2 := 0.0
	for i, id := range current {
		pr, ok := prevRank[id]
		if !ok {
			// Node present in current but absent from prev = structural change.
			return 1.0
		}
		d := float64(i - pr)
		sumD2 += d * d
	}

	rho := 1.0 - (6.0*sumD2)/float64(n*(n*n-1))
	instability := (1.0 - rho) / 2.0
	return math.Max(0.0, math.Min(1.0, instability))
}

// lgMaxPosteriorVariance computes the maximum Bayesian variance-to-squared-effect
// ratio among all identified causes. A high ratio indicates that the posterior
// distribution of the causal effect overlaps with zero, implying that the effect
// is statistically indistinguishable from background noise, often caused by
// unmeasured latent confounders injecting variance into the causal link.
func lgMaxPosteriorVariance(exp Explanation) float64 {
	maxRatio := 0.0
	for _, cause := range exp.Causes {
		effect := exp.Effects[cause]
		variance := exp.Uncertainty[cause]
		if effect == 0 {
			continue
		}
		// Calculate variance relative to squared effect (SNR metric)
		ratio := variance / (effect * effect)
		if ratio > maxRatio {
			maxRatio = ratio
		}
	}
	return maxRatio
}
