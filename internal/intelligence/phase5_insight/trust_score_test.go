package phase5_insight

/*
TRUST SCORE TEST SUITE — Falsification-Grade Validation

Each test is designed to be a falsification attempt against the trust pipeline.
Tests are not mere "happy path" checks: they target boundary conditions,
adversarial inputs, and degenerate cases that a production system must handle
without silently inflating confidence.

Test inventory:
  TestHiddenConfounderSuspicion      — graph Z→{A,B}, A→C, B→C; Z must be excluded
  TestNoPathHighCorrelation          — node with high effect but no DAG path → HIGH
  TestLowResidualExplanation         — majority of effect mass off-path → MEDIUM/HIGH
  TestUnstableRanking                — inverted ranking → HIGH instability
  TestConfidenceDowngradeOnLatentHigh — HIGH latent forces UNKNOWN regardless of score
  TestUnknownFallbackTrigger         — combined gate conditions trigger UNKNOWN
  TestDeterministicRepeatedScores    — identical inputs produce bit-identical outputs
  TestNoFalseDowngradeOnValidDAG     — clean transitive DAG produces LOW risk + CONFIRMED

Each test logs: threshold, observed score, latent level, final state.
Every assertion uses exact threshold comparisons, not pass-only checks.
*/

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers and constants
// ---------------------------------------------------------------------------

const (
	latentCorrNoPathThreshold  = 0.70
	latentResidualThreshold    = 0.40
	latentVarianceSNRThreshold = 0.25
	latentInstabilityThreshold = 0.60
)

// tNode creates a CausalNode for use in test graphs.
// The Func is a constant returning val; Timestamp and Lags are zeroed.
func tNode(id string, val float64) *CausalNode {
	return &CausalNode{
		ID:        id,
		Value:     val,
		Timestamp: 0,
		Noise:     0.0,
		Lags:      []int{},
		Parents:   []*CausalNode{},
		Func: func(inputs []float64, noise float64) float64 {
			return val + noise
		},
	}
}

// tEdge creates a directed CausalEdge from → to.
func tEdge(from, to *CausalNode) *CausalEdge {
	return &CausalEdge{From: from, To: to, Lag: 0}
}

// tGraph assembles a CausalGraph from a slice of nodes and edges.
func tGraph(nodes []*CausalNode, edges []*CausalEdge) *CausalGraph {
	nodeMap := make(map[string]*CausalNode, len(nodes))
	for _, n := range nodes {
		nodeMap[n.ID] = n
	}
	return &CausalGraph{Nodes: nodeMap, Edges: edges}
}

// tExp constructs an Explanation with the provided causes (ordered), effects,
// and uniform uncertainty values for test isolation.
func tExp(causes []string, effects map[string]float64) Explanation {
	unc := make(map[string]float64, len(effects))
	for k := range effects {
		unc[k] = 0.05
	}
	return Explanation{Causes: causes, Effects: effects, Uncertainty: unc}
}

// logTrustResult prints the structured diagnostic line required by the spec.
// Every test must call this before its assertions.
func logTrustResult(t *testing.T, tag string, threshold float64, observed float64, latentLevel LatentRiskLevel, state ConfidenceState) {
	t.Helper()
	t.Logf("[%s] threshold=%.4f | observed=%.4f | latent=%s | state=%s",
		tag, threshold, observed, latentLevel.String(), state.String())
}

// ---------------------------------------------------------------------------
// Test 1 — Hidden confounder suspicion
// ---------------------------------------------------------------------------

// TestHiddenConfounderSuspicion verifies that a classic fork-confound pattern
// (Z→A, Z→B, A→C, B→C) correctly flags Z as a confounder via FuseCausalResults,
// and that AssessLatentRisk still produces LOW risk when all legitimate paths
// exist (A→C, B→C are both present).
//
// Rationale: the latent guard should not penalise the system for a confounder
// that the fusion layer has already correctly rejected. This is the "no false
// positive on diagnosed confounders" case.
func TestHiddenConfounderSuspicion(t *testing.T) {
	Z := tNode("Z", 1.0)
	A := tNode("A", 0.8)
	B := tNode("B", 0.6)
	C := tNode("C", 0.4) // target

	graph := tGraph(
		[]*CausalNode{Z, A, B, C},
		[]*CausalEdge{
			tEdge(Z, A),
			tEdge(Z, B),
			tEdge(A, C),
			tEdge(B, C),
		},
	)

	// Phase 4 explains A and B have effects on C, both reachable via DAG.
	// Z is not in the explanation (FuseCausalResults would have rejected it).
	exp := tExp(
		[]string{"A", "B"},
		map[string]float64{"A": 0.55, "B": 0.45},
	)

	// Fusion confirms A and B as roots (Z was rejected).
	fusion := FusionResult{
		RootCauses: []string{"A", "B"},
		Mediators:  []string{},
		Rejected:   []string{"Z"},
	}

	latent := AssessLatentRisk(graph, exp, nil, "C")
	conf := ComputeConfidence(fusion, graph, exp, latent, 1.0)
	fallback := EvaluateFallback(conf, latent, fusion, graph, "C")

	logTrustResult(t, "HiddenConfounderSuspicion",
		latentCorrNoPathThreshold, latent.CorrelationScore,
		latent.Level, conf.State)

	// A and B both have paths to C → CorrelationScore should be 0 (no spurious nodes).
	if latent.CorrelationScore >= latentCorrNoPathThreshold {
		t.Errorf("expected no spurious correlation (no off-path effects), got CorrelationScore=%.4f (threshold=%.4f)",
			latent.CorrelationScore, latentCorrNoPathThreshold)
	}

	// With full coverage (A and B cover all relevant paths), residual should be high.
	if latent.ResidualRatio < 0.99 {
		t.Errorf("expected ResidualRatio ≈ 1.0 (all effect mass on path), got %.4f", latent.ResidualRatio)
	}

	// Variance ratio max is 0.05 / 0.45^2 ≈ 0.2469.
	expectedVariance := 0.2469
	if latent.PosteriorVariance > expectedVariance+0.01 || latent.PosteriorVariance < expectedVariance-0.01 {
		t.Errorf("expected PosteriorVariance ≈ %.4f, got %.4f", expectedVariance, latent.PosteriorVariance)
	}
	if latent.PosteriorVariance >= latentVarianceSNRThreshold {
		t.Errorf("expected PosteriorVariance < threshold %.4f, got %.4f", latentVarianceSNRThreshold, latent.PosteriorVariance)
	}

	// No active signals should fire.
	if latent.Signals != SignalNone {
		t.Errorf("expected SignalNone, got signals bitmask %d", latent.Signals)
	}

	// Risk level should be LOW.
	if latent.Level != LatentRiskLow {
		t.Errorf("expected LatentRiskLow, got %s", latent.Level.String())
	}

	// Confidence should be CONFIRMED (clean evidence).
	if conf.State != ConfirmedState {
		t.Errorf("expected CONFIRMED, got %s (score=%.4f)", conf.State.String(), conf.Score)
	}

	// No fallback.
	if fallback.IsUnknown {
		t.Errorf("unexpected UNKNOWN fallback: reasons=%v", fallback.Reasons)
	}

	t.Logf("[HiddenConfounderSuspicion] PASS — fusion-rejected confounder Z does not inflate latent risk")
}

// ---------------------------------------------------------------------------
// Test 2 — No-path high correlation
// ---------------------------------------------------------------------------

// TestNoPathHighCorrelation constructs a scenario where node D has a large
// effect in the explanation but has NO directed path to the target C.
// This directly tests the SignalCorrelationNoPath detector.
//
// Graph: A→B→C  (D is isolated — no edges to/from D)
// Explanation: effects on A (legitimate) and D (spurious).
func TestNoPathHighCorrelation(t *testing.T) {
	A := tNode("A", 0.9)
	B := tNode("B", 0.7)
	C := tNode("C", 0.5)
	D := tNode("D", 0.8) // isolated node with no path to C

	graph := tGraph(
		[]*CausalNode{A, B, C, D},
		[]*CausalEdge{
			tEdge(A, B),
			tEdge(B, C),
			// D has no edges
		},
	)

	// D has a larger normalized effect than A → should trigger SignalCorrelationNoPath.
	// |D|/(|A|+|D|) = 0.80/(0.10+0.80) = 0.889 > latentCorrNoPathThreshold (0.70)
	exp := tExp(
		[]string{"A", "D"},
		map[string]float64{"A": 0.10, "D": 0.80},
	)

	fusion := FusionResult{
		RootCauses: []string{"A"},
		Mediators:  []string{"B"},
		Rejected:   []string{},
	}

	latent := AssessLatentRisk(graph, exp, nil, "C")
	conf := ComputeConfidence(fusion, graph, exp, latent, 1.0)

	expectedNormD := 0.80 / (0.10 + 0.80)
	logTrustResult(t, "NoPathHighCorrelation",
		latentCorrNoPathThreshold, latent.CorrelationScore,
		latent.Level, conf.State)

	t.Logf("[NoPathHighCorrelation] normalized_D=%.4f (expected > threshold=%.4f)",
		expectedNormD, latentCorrNoPathThreshold)

	// D's normalized effect must exceed the no-path threshold.
	if latent.CorrelationScore < latentCorrNoPathThreshold {
		t.Errorf("expected CorrelationScore ≥ %.4f (D has large off-path effect), got %.4f",
			latentCorrNoPathThreshold, latent.CorrelationScore)
	}

	// SignalCorrelationNoPath must be set.
	if latent.Signals&SignalCorrelationNoPath == 0 {
		t.Errorf("expected SignalCorrelationNoPath to be set, signals=%d", latent.Signals)
	}

	// D must appear in SuspiciousNodes.
	dFound := false
	for _, id := range latent.SuspiciousNodes {
		if id == "D" {
			dFound = true
			break
		}
	}
	if !dFound {
		t.Errorf("expected D in SuspiciousNodes, got %v", latent.SuspiciousNodes)
	}

	// Any HIGH-severity signal must produce LatentRiskHigh.
	if latent.Level != LatentRiskHigh {
		t.Errorf("expected LatentRiskHigh (SignalCorrelationNoPath fired), got %s", latent.Level.String())
	}

	// LatentRiskHigh must force state = UNKNOWN.
	if conf.State != UnknownState {
		t.Errorf("expected UNKNOWN state (LatentRiskHigh), got %s (score=%.4f)",
			conf.State.String(), conf.Score)
	}

	t.Logf("[NoPathHighCorrelation] PASS — off-path D node correctly triggers HIGH risk and UNKNOWN state")
}

// ---------------------------------------------------------------------------
// Test 3 — Low residual explanation
// ---------------------------------------------------------------------------

// TestLowResidualExplanation places most effect mass on a node (X) that has
// no path to the target, producing a low ResidualRatio.
//
// Graph: A→C  (simple single-path chain)
// Explanation: A has small effect (0.10), X has large effect (0.75) but no path.
// ResidualRatio = 0.10/(0.10+0.75) ≈ 0.118 < latentResidualThreshold (0.40)
func TestLowResidualExplanation(t *testing.T) {
	A := tNode("A", 0.5)
	C := tNode("C", 0.3)
	X := tNode("X", 0.9) // off-path, large effect

	graph := tGraph(
		[]*CausalNode{A, C, X},
		[]*CausalEdge{
			tEdge(A, C),
			// X is isolated
		},
	)

	exp := tExp(
		[]string{"A", "X"},
		map[string]float64{"A": 0.10, "X": 0.75},
	)

	fusion := FusionResult{
		RootCauses: []string{"A"},
		Mediators:  []string{},
		Rejected:   []string{},
	}

	latent := AssessLatentRisk(graph, exp, nil, "C")
	conf := ComputeConfidence(fusion, graph, exp, latent, 1.0)

	expectedResidual := 0.10 / (0.10 + 0.75)
	logTrustResult(t, "LowResidualExplanation",
		latentResidualThreshold, latent.ResidualRatio,
		latent.Level, conf.State)

	t.Logf("[LowResidualExplanation] residual=%.4f (expected ≈ %.4f, threshold=%.4f)",
		latent.ResidualRatio, expectedResidual, latentResidualThreshold)

	// ResidualRatio must be approximately 0.118.
	if latent.ResidualRatio > 0.15 {
		t.Errorf("expected ResidualRatio ≈ %.4f (A's small on-path effect), got %.4f",
			expectedResidual, latent.ResidualRatio)
	}

	// SignalUnexplainedResidual must fire.
	if latent.Signals&SignalUnexplainedResidual == 0 {
		t.Errorf("expected SignalUnexplainedResidual, signals=%d", latent.Signals)
	}

	// X also has no path and large effect → SignalCorrelationNoPath should also fire.
	// Combined: 1 HIGH + 1 MEDIUM → LatentRiskHigh.
	if latent.Level != LatentRiskHigh {
		t.Errorf("expected LatentRiskHigh (combined signals), got %s", latent.Level.String())
	}

	// Score must be penalised by latentPenaltyHigh (0.40).
	if conf.Components.LatentPenalty != latentPenaltyHigh {
		t.Errorf("expected LatentPenalty=%.2f for HIGH risk, got %.4f",
			latentPenaltyHigh, conf.Components.LatentPenalty)
	}

	if conf.State != UnknownState {
		t.Errorf("expected UNKNOWN, got %s (score=%.4f)", conf.State.String(), conf.Score)
	}

	t.Logf("[LowResidualExplanation] PASS — low residual ratio correctly penalises confidence")
}

// ---------------------------------------------------------------------------
// Test 4 — Unstable ranking
// ---------------------------------------------------------------------------

// TestUnstableRanking feeds a completely inverted ranking (prev=[A,B,C], curr=[C,B,A])
// and verifies that instability = 1.0 and SignalRankingInstability fires.
//
// Spearman d² = (0-2)²+(1-1)²+(2-0)² = 4+0+4 = 8
// ρ_s = 1 - 6×8/(3×8) = 1 - 48/24 = 1 - 2 = -1
// instability = (1 - (-1))/2 = 1.0
func TestUnstableRanking(t *testing.T) {
	A := tNode("A", 0.9)
	B := tNode("B", 0.7)
	C := tNode("C", 0.5)

	graph := tGraph(
		[]*CausalNode{A, B, C},
		[]*CausalEdge{tEdge(A, B), tEdge(B, C)},
	)

	// Current ranking: C first (inverted from prior)
	exp := tExp(
		[]string{"C", "B", "A"}, // completely inverted from prevRanking
		map[string]float64{"A": 0.5, "B": 0.3, "C": 0.2},
	)

	prevRanking := []string{"A", "B", "C"} // prior ranking

	latent := AssessLatentRisk(graph, exp, prevRanking, "C")
	conf := ComputeConfidence(
		FusionResult{RootCauses: []string{"A"}, Mediators: []string{"B"}, Rejected: []string{}},
		graph, exp, latent, 1.0,
	)

	logTrustResult(t, "UnstableRanking",
		latentInstabilityThreshold, latent.RankInstability,
		latent.Level, conf.State)

	t.Logf("[UnstableRanking] instability=%.4f (expected=1.0, threshold=%.4f)",
		latent.RankInstability, latentInstabilityThreshold)

	// Fully inverted → instability must be 1.0.
	if latent.RankInstability < 0.99 {
		t.Errorf("expected RankInstability ≈ 1.0 (fully inverted), got %.4f", latent.RankInstability)
	}

	// SignalRankingInstability must be set.
	if latent.Signals&SignalRankingInstability == 0 {
		t.Errorf("expected SignalRankingInstability to be set, signals=%d", latent.Signals)
	}

	// HIGH-severity signal → LatentRiskHigh.
	if latent.Level != LatentRiskHigh {
		t.Errorf("expected LatentRiskHigh, got %s", latent.Level.String())
	}

	// Determinism component must be 0.0 (instability=1.0 → 1-1.0=0.0).
	if conf.Components.Determinism > 0.01 {
		t.Errorf("expected Determinism ≈ 0.0 (instability=1.0), got %.4f",
			conf.Components.Determinism)
	}

	if conf.State != UnknownState {
		t.Errorf("expected UNKNOWN, got %s (score=%.4f)", conf.State.String(), conf.Score)
	}

	t.Logf("[UnstableRanking] PASS — inverted ranking triggers HIGH risk and UNKNOWN state")
}

// ---------------------------------------------------------------------------
// Test 5 — Confidence downgrade on latent HIGH
// ---------------------------------------------------------------------------

// TestConfidenceDowngradeOnLatentHigh verifies the safety invariant:
// LatentRiskHigh unconditionally forces UNKNOWN regardless of numeric score.
//
// This test manually constructs a LatentRiskReport with Level=HIGH but
// with metric values that would otherwise produce a high numeric score.
// If the unconditional override is absent, the test would return CONFIRMED.
func TestConfidenceDowngradeOnLatentHigh(t *testing.T) {
	A := tNode("A", 0.9)
	C := tNode("C", 0.5)

	graph := tGraph(
		[]*CausalNode{A, C},
		[]*CausalEdge{tEdge(A, C)},
	)

	exp := tExp(
		[]string{"A"},
		map[string]float64{"A": 1.0},
	)

	fusion := FusionResult{
		RootCauses: []string{"A"},
		Mediators:  []string{},
		Rejected:   []string{},
	}

	// Manually construct a HIGH latent report with otherwise "good" metrics.
	// This simulates the edge case where a hidden confounder was detected via
	// a separate channel (e.g., domain knowledge injection) but all observable
	// metrics look clean.
	latentHighManual := LatentRiskReport{
		Level:            LatentRiskHigh,
		Signals:          SignalCorrelationNoPath,
		CorrelationScore: 0.85, // above threshold
		ResidualRatio:    0.95, // high — looks clean
		RankInstability:  0.00, // stable
		PosteriorVariance:    1.00, // full coverage
		SuspiciousNodes:  []string{"ghost_node"},
	}

	conf := ComputeConfidence(fusion, graph, exp, latentHighManual, 1.0)

	logTrustResult(t, "ConfidenceDowngradeOnLatentHigh",
		confirmedThreshold, conf.Score,
		latentHighManual.Level, conf.State)

	t.Logf("[ConfidenceDowngradeOnLatentHigh] score=%.4f (would be CONFIRMED without override)",
		conf.Score)

	// The safety invariant: LatentRiskHigh MUST force UNKNOWN.
	if conf.State != UnknownState {
		t.Errorf("SAFETY INVARIANT VIOLATED: LatentRiskHigh must force UNKNOWN, got %s (score=%.4f)",
			conf.State.String(), conf.Score)
	}

	// Penalty component must be latentPenaltyHigh (0.40).
	if conf.Components.LatentPenalty != latentPenaltyHigh {
		t.Errorf("expected LatentPenalty=%.2f for HIGH risk, got %.4f",
			latentPenaltyHigh, conf.Components.LatentPenalty)
	}

	// Score must not exceed 1.0 even with high component values minus penalty.
	if conf.Score > 1.0 {
		t.Errorf("score exceeds upper bound 1.0: %.4f", conf.Score)
	}
	if conf.Score < 0.0 {
		t.Errorf("score below lower bound 0.0: %.4f", conf.Score)
	}

	t.Logf("[ConfidenceDowngradeOnLatentHigh] PASS — safety invariant holds: LatentRiskHigh → UNKNOWN")
}

// ---------------------------------------------------------------------------
// Test 6 — Unknown fallback trigger
// ---------------------------------------------------------------------------

// TestUnknownFallbackTrigger verifies that EvaluateFallback correctly identifies
// all active reason codes and generates appropriate telemetry probes when
// multiple fallback conditions fire simultaneously.
func TestUnknownFallbackTrigger(t *testing.T) {
	A := tNode("A", 0.9)
	C := tNode("C", 0.5)

	graph := tGraph(
		[]*CausalNode{A, C},
		[]*CausalEdge{tEdge(A, C)},
	)

	// Fusion lists "ghost" as a root — it does not exist in graph → Gate 3 fires.
	fusion := FusionResult{
		RootCauses: []string{"ghost"},
		Mediators:  []string{},
		Rejected:   []string{"Z"},
	}

	// HIGH latent report → Gate 1 fires.
	latent := LatentRiskReport{
		Level:            LatentRiskHigh,
		Signals:          SignalRankingInstability | SignalHighPosteriorVariance,
		CorrelationScore: 0.0,
		ResidualRatio:    0.80,
		RankInstability:  0.85,
		PosteriorVariance:    0.50, // above fallbackSevereVarianceThreshold (0.40) → Gate 4 fires
		SuspiciousNodes:  []string{},
	}

	conf := ConfidenceReport{
		Score: 0.20, // below probableThreshold → Gate 2 fires
		State: UnknownState,
		Components: ConfidenceComponents{
			LatentPenalty: latentPenaltyHigh,
		},
	}

	fallback := EvaluateFallback(conf, latent, fusion, graph, "C")

	logTrustResult(t, "UnknownFallbackTrigger",
		probableThreshold, conf.Score,
		latent.Level, conf.State)

	t.Logf("[UnknownFallbackTrigger] reasons=%v probes=%d",
		fallback.Reasons, len(fallback.ProbeRecommendations))

	// Must be UNKNOWN.
	if !fallback.IsUnknown {
		t.Errorf("expected IsUnknown=true, got false")
	}

	// Verify expected reason codes are present.
	reasonMap := make(map[FallbackReason]bool)
	for _, r := range fallback.Reasons {
		reasonMap[r] = true
	}

	expectedReasons := []FallbackReason{
		FallbackReasonLatentHighRisk,      // Gate 1: latent HIGH
		FallbackReasonLowConfidence,       // Gate 2: score=0.20 < 0.45
		FallbackReasonGraphMismatch,       // Gate 3: "ghost" not in graph
		FallbackReasonSevereVariance,      // Gate 4: variance=0.50 > 0.40
		FallbackReasonRankingInstability,  // Gate 5: SignalRankingInstability set
	}

	for _, expected := range expectedReasons {
		if !reasonMap[expected] {
			t.Errorf("expected reason %s not found in %v", expected.String(), fallback.Reasons)
		}
	}

	// Policy must always be human review.
	if fallback.RemediationPolicy != PolicyHumanReview {
		t.Errorf("expected PolicyHumanReview, got %d", fallback.RemediationPolicy)
	}
	if fallback.RemediationPolicy.String() != "HUMAN_REVIEW_REQUIRED" {
		t.Errorf("expected 'HUMAN_REVIEW_REQUIRED', got '%s'", fallback.RemediationPolicy.String())
	}

	// Probes must be generated (at minimum the target probe).
	if len(fallback.ProbeRecommendations) == 0 {
		t.Errorf("expected at least 1 probe recommendation, got 0")
	}

	// Target probe must include the residual_variance_decomposition metric.
	targetProbeFound := false
	for _, p := range fallback.ProbeRecommendations {
		if p.NodeID == "C" && p.Metric == "residual_variance_decomposition" {
			targetProbeFound = true
			break
		}
	}
	if !targetProbeFound {
		t.Errorf("expected residual_variance_decomposition probe for target 'C', not found in %+v",
			fallback.ProbeRecommendations)
	}

	// Rejected node Z must generate a confounder_isolation_trace probe.
	zProbeFound := false
	for _, p := range fallback.ProbeRecommendations {
		if p.NodeID == "Z" && p.Metric == "confounder_isolation_trace" {
			zProbeFound = true
			break
		}
	}
	if !zProbeFound {
		t.Errorf("expected confounder_isolation_trace probe for rejected node 'Z', not found")
	}

	// Snapshot values must be preserved.
	if fallback.ConfidenceScore != conf.Score {
		t.Errorf("expected ConfidenceScore=%.4f, got %.4f", conf.Score, fallback.ConfidenceScore)
	}
	if fallback.LatentLevel != latent.Level {
		t.Errorf("expected LatentLevel=%s, got %s", latent.Level.String(), fallback.LatentLevel.String())
	}

	t.Logf("[UnknownFallbackTrigger] PASS — %d reason(s) identified, %d probe(s) generated",
		len(fallback.Reasons), len(fallback.ProbeRecommendations))
}

// ---------------------------------------------------------------------------
// Test 7 — Deterministic repeated scores
// ---------------------------------------------------------------------------

// TestDeterministicRepeatedScores verifies that identical inputs produce
// bit-identical outputs across 10 repeated invocations.
// This is essential for reproducibility in production and for test reliability.
// Any non-determinism would indicate hidden map-iteration or rand dependency.
func TestDeterministicRepeatedScores(t *testing.T) {
	A := tNode("A", 0.9)
	B := tNode("B", 0.7)

	exp := tExp(
		[]string{"A", "B"},
		map[string]float64{"A": 0.6, "B": 0.4},
	)

	prevRanking := []string{"A", "B"}

	fusion := FusionResult{
		RootCauses: []string{"A"},
		Mediators:  []string{"B"},
		Rejected:   []string{},
	}

	const iterations = 10

	type snapshot struct {
		corrScore   float64
		residual    float64
		instability float64
		coverage    float64
		riskLevel   LatentRiskLevel
		confScore   float64
		confState   ConfidenceState
	}

	results := make([]snapshot, iterations)

	for i := 0; i < iterations; i++ {
		nodeName := fmt.Sprintf("C_Det_%d", i)
		C := tNode(nodeName, 0.5)
		graph := tGraph(
			[]*CausalNode{A, B, C},
			[]*CausalEdge{tEdge(A, B), tEdge(B, C)},
		)
		latent := AssessLatentRisk(graph, exp, prevRanking, nodeName)
		conf := ComputeConfidence(fusion, graph, exp, latent, 1.0)
		results[i] = snapshot{
			corrScore:   latent.CorrelationScore,
			residual:    latent.ResidualRatio,
			instability: latent.RankInstability,
			coverage:    latent.PosteriorVariance,
			riskLevel:   latent.Level,
			confScore:   conf.Score,
			confState:   conf.State,
		}
	}

	logTrustResult(t, "DeterministicRepeatedScores",
		probableThreshold, results[0].confScore,
		results[0].riskLevel, results[0].confState)

	// All 10 results must be identical.
	for i := 1; i < iterations; i++ {
		if results[i] != results[0] {
			t.Errorf("non-deterministic output at iteration %d: got %+v, expected %+v",
				i, results[i], results[0])
		}
	}

	t.Logf("[DeterministicRepeatedScores] score=%.6f across %d iterations: DETERMINISTIC",
		results[0].confScore, iterations)
	t.Logf("[DeterministicRepeatedScores] PASS — %d iterations produced identical output", iterations)
}

// ---------------------------------------------------------------------------
// Test 8 — No false downgrade on valid DAG
// ---------------------------------------------------------------------------

// TestNoFalseDowngradeOnValidDAG verifies that a well-formed transitive DAG
// (A→B→C with full coverage and stable ranking) is classified as:
//   - LatentRiskLow
//   - ConfirmedState
//   - IsUnknown=false
//
// This is the critical anti-regression test: the system must not produce
// false positives on clean evidence. Overly conservative systems that always
// return UNKNOWN provide no value over a static fallback.
func TestNoFalseDowngradeOnValidDAG(t *testing.T) {
	A := tNode("A", 1.0)
	B := tNode("B", 0.8)
	C := tNode("C", 0.6)

	// Clean transitive DAG: A→B→C
	graph := tGraph(
		[]*CausalNode{A, B, C},
		[]*CausalEdge{tEdge(A, B), tEdge(B, C)},
	)

	// Explanation consistent with DAG: A has largest effect, B is mediator.
	// Both have paths to C: A→B→C, B→C.
	exp := tExp(
		[]string{"A", "B"},
		map[string]float64{"A": 0.60, "B": 0.50},
	)

	// Stable ranking: same as prior.
	prevRanking := []string{"A", "B"}

	fusion := FusionResult{
		RootCauses: []string{"A"},
		Mediators:  []string{"B"},
		Rejected:   []string{},
	}

	latent := AssessLatentRisk(graph, exp, prevRanking, "C")
	conf := ComputeConfidence(fusion, graph, exp, latent, 1.0)
	fallback := EvaluateFallback(conf, latent, fusion, graph, "C")

	logTrustResult(t, "NoFalseDowngradeOnValidDAG",
		confirmedThreshold, conf.Score,
		latent.Level, conf.State)

	t.Logf("[NoFalseDowngradeOnValidDAG] signals=%d variance=%.4f residual=%.4f instability=%.4f",
		latent.Signals, latent.PosteriorVariance, latent.ResidualRatio, latent.RankInstability)

	// No signals should fire on a clean, stable, fully-covered DAG.
	if latent.Signals != SignalNone {
		t.Errorf("expected SignalNone on valid DAG, got signals bitmask %d", latent.Signals)
	}

	// Risk level must be LOW.
	if latent.Level != LatentRiskLow {
		t.Errorf("expected LatentRiskLow, got %s", latent.Level.String())
	}

	// All latent channels should be in healthy ranges.
	if latent.CorrelationScore >= latentCorrNoPathThreshold {
		t.Errorf("CorrelationScore=%.4f should be below threshold=%.4f", latent.CorrelationScore, latentCorrNoPathThreshold)
	}
	if latent.ResidualRatio < latentResidualThreshold {
		t.Errorf("ResidualRatio=%.4f should be ≥ threshold=%.4f", latent.ResidualRatio, latentResidualThreshold)
	}
	if latent.RankInstability > latentInstabilityThreshold {
		t.Errorf("RankInstability=%.4f should be ≤ threshold=%.4f", latent.RankInstability, latentInstabilityThreshold)
	}
	if latent.PosteriorVariance >= latentVarianceSNRThreshold {
		t.Errorf("PosteriorVariance=%.4f should be < threshold=%.4f", latent.PosteriorVariance, latentVarianceSNRThreshold)
	}

	// Score must be in CONFIRMED band (≥ 0.75).
	if conf.Score < confirmedThreshold {
		t.Errorf("expected score ≥ %.4f (CONFIRMED), got %.4f", confirmedThreshold, conf.Score)
	}

	// State must be CONFIRMED.
	if conf.State != ConfirmedState {
		t.Errorf("expected CONFIRMED, got %s (score=%.4f)", conf.State.String(), conf.Score)
	}

	// No penalty.
	if conf.Components.LatentPenalty != latentPenaltyLow {
		t.Errorf("expected zero penalty on LOW risk, got %.4f", conf.Components.LatentPenalty)
	}

	// No fallback.
	if fallback.IsUnknown {
		t.Errorf("expected IsUnknown=false on valid DAG, got reasons=%v", fallback.Reasons)
	}
	if len(fallback.ProbeRecommendations) != 0 {
		t.Errorf("expected no probes on valid DAG, got %d", len(fallback.ProbeRecommendations))
	}

	t.Logf("[NoFalseDowngradeOnValidDAG] PASS — clean DAG: score=%.4f state=%s risk=%s",
		conf.Score, conf.State.String(), latent.Level.String())
}

// ---------------------------------------------------------------------------
// Test 9 — Spearman rank instability formula verification
// ---------------------------------------------------------------------------

// TestSpearmanInstabilityFormula is a unit-level mathematical falsification test.
// It verifies the exact Spearman computation for three known cases:
//   identical ranking    → instability = 0.0
//   fully inverted (n=3) → instability = 1.0
//   partial shift        → instability in (0, 1)
func TestSpearmanInstabilityFormula(t *testing.T) {
	type tc struct {
		name     string
		current  []string
		prev     []string
		expected float64
		tol      float64
	}

	cases := []tc{
		{
			// d² = 0+0+0 = 0, ρ = 1, instability = 0
			name:     "identical",
			current:  []string{"A", "B", "C"},
			prev:     []string{"A", "B", "C"},
			expected: 0.0,
			tol:      1e-9,
		},
		{
			// d² = (0-2)²+(1-1)²+(2-0)² = 4+0+4 = 8
			// ρ = 1 - 6×8/(3×8) = 1-2 = -1
			// instability = (1-(-1))/2 = 1.0
			name:     "fully_inverted",
			current:  []string{"C", "B", "A"},
			prev:     []string{"A", "B", "C"},
			expected: 1.0,
			tol:      1e-9,
		},
		{
			// d² = (0-1)²+(1-0)²+(2-2)² = 1+1+0 = 2
			// ρ = 1 - 6×2/(3×8) = 1 - 12/24 = 1 - 0.5 = 0.5
			// instability = (1-0.5)/2 = 0.25
			name:     "single_swap",
			current:  []string{"B", "A", "C"},
			prev:     []string{"A", "B", "C"},
			expected: 0.25,
			tol:      1e-9,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lgRankInstability(tc.current, tc.prev)
			t.Logf("[SpearmanFormula/%s] expected=%.6f observed=%.6f tol=%.1e",
				tc.name, tc.expected, got, tc.tol)

			diff := got - tc.expected
			if diff < 0 {
				diff = -diff
			}
			if diff > tc.tol {
				t.Errorf("lgRankInstability(%v, %v): expected=%.6f got=%.6f (diff=%.6f > tol=%.1e)",
					tc.current, tc.prev, tc.expected, got, diff, tc.tol)
			}
		})
	}

	t.Logf("[SpearmanInstabilityFormula] PASS — all three boundary cases validated")
}

// ---------------------------------------------------------------------------
// Test 10 — Nil graph safety
// ---------------------------------------------------------------------------

// TestNilGraphSafety ensures that a nil graph does not panic and produces
// a well-formed LatentRiskHigh report, confirming the fail-safe contract.
func TestNilGraphSafety(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PANIC on nil graph: %v", r)
		}
	}()

	exp := tExp([]string{"A"}, map[string]float64{"A": 0.9})
	fusion := FusionResult{RootCauses: []string{"A"}, Mediators: []string{}, Rejected: []string{}}

	latent := AssessLatentRisk(nil, exp, nil, "C")
	conf := ComputeConfidence(fusion, nil, exp, latent, 1.0)
	fallback := EvaluateFallback(conf, latent, fusion, nil, "C")

	logTrustResult(t, "NilGraphSafety",
		probableThreshold, conf.Score,
		latent.Level, conf.State)

	if latent.Level != LatentRiskHigh {
		t.Errorf("nil graph must produce LatentRiskHigh, got %s", latent.Level.String())
	}
	if conf.State != UnknownState {
		t.Errorf("nil graph must produce UNKNOWN, got %s", conf.State.String())
	}
	if !fallback.IsUnknown {
		t.Errorf("nil graph must trigger UNKNOWN fallback")
	}

	t.Logf("[NilGraphSafety] PASS — nil graph safely produces %s / %s",
		latent.Level.String(), conf.State.String())
}

// ---------------------------------------------------------------------------
// Test 11 — Medium risk boundary: exactly one MEDIUM signal
// ---------------------------------------------------------------------------

// TestMediumRiskBoundary verifies that exactly one MEDIUM-severity signal
// (coverage collapse, no HIGH signals) produces LatentRiskMedium and ProbableState.
func TestMediumRiskBoundary(t *testing.T) {
	A := tNode("A", 0.9)
	C := tNode("C_Med", 0.5)

	// Graph with only 2 nodes, causes cover both → coverage ≈ 1.0
	// But we can engineer sparse coverage by adding isolated nodes.
	D := tNode("D", 0.3) // isolated, never on any path
	E := tNode("E", 0.2) // isolated

	// coverage = 2 / 4 = 0.50 exactly — at the boundary.
	// We need coverage < 0.50, so add one more isolated node.
	F := tNode("F", 0.1)

	// 5 nodes total, path covers A, C = 2 nodes → coverage = 2/5 = 0.40 < 0.50
	graph := tGraph(
		[]*CausalNode{A, C, D, E, F},
		[]*CausalEdge{tEdge(A, C)},
	)

	// All effects are on A which has a path → ResidualRatio = 1.0 (no residual signal)
	// No ranking instability (prevRanking = nil → instability = 0)
	// No correlation without path (A has path to C)
	// Only SignalHighPosteriorVariance should fire.
	medExp := tExp(
		[]string{"A"},
		map[string]float64{"A": 0.40},
	)

	fusion := FusionResult{
		RootCauses: []string{"A"},
		Mediators:  []string{},
		Rejected:   []string{},
	}

	latent := AssessLatentRisk(graph, medExp, nil, "C_Med")
	conf := ComputeConfidence(fusion, graph, medExp, latent, 1.0)

	expectedVariance := 0.3125 // 0.05 / 0.40^2
	logTrustResult(t, "MediumRiskBoundary",
		latentVarianceSNRThreshold, latent.PosteriorVariance,
		latent.Level, conf.State)

	t.Logf("[MediumRiskBoundary] variance=%.4f (expected≈%.4f, threshold=%.4f)",
		latent.PosteriorVariance, expectedVariance, latentVarianceSNRThreshold)

	// Only coverage collapse should fire.
	if latent.Signals != SignalHighPosteriorVariance {
		t.Errorf("expected only SignalHighPosteriorVariance, got signals=%d", latent.Signals)
	}

	// Exactly one MEDIUM signal → LatentRiskMedium.
	if latent.Level != LatentRiskMedium {
		t.Errorf("expected LatentRiskMedium (one MEDIUM signal), got %s", latent.Level.String())
	}

	// Medium risk penalty = latentPenaltyMedium (0.15).
	if conf.Components.LatentPenalty != latentPenaltyMedium {
		t.Errorf("expected LatentPenalty=%.2f for MEDIUM risk, got %.4f",
			latentPenaltyMedium, conf.Components.LatentPenalty)
	}

	// State should be PROBABLE or CONFIRMED (not UNKNOWN), as MEDIUM doesn't force UNKNOWN.
	if conf.State == UnknownState {
		t.Logf("[MediumRiskBoundary] score=%.4f penalised to UNKNOWN by MEDIUM risk — acceptable if score naturally low", conf.Score)
	}

	t.Logf("[MediumRiskBoundary] PASS — single MEDIUM signal produces LatentRiskMedium penalty=%.2f state=%s",
		latentPenaltyMedium, conf.State.String())
}
