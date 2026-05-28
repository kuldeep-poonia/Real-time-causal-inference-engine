package orchestrator

import (
	"fmt"
	"math"
	"sort"
	"strings"

	phase2 "absia/internal/intelligence/phase2_pattern"
	phase3 "absia/internal/intelligence/phase3_causal"
)

type OperationalState string

const (
	StateHealthy       OperationalState = "HEALTHY"
	StateWatch         OperationalState = "WATCH"
	StateDegraded      OperationalState = "DEGRADED"
	StateUnstable      OperationalState = "UNSTABLE"
	StateCascadeRisk   OperationalState = "CASCADE RISK"
	StatePartialOutage OperationalState = "PARTIAL OUTAGE"
	StateCritical      OperationalState = "CRITICAL"
	StateRecovering    OperationalState = "RECOVERING"
)

type FailureCategory string

const (
	CategoryRetryStorm           FailureCategory = "Retry Storm"
	CategoryQueueSaturation      FailureCategory = "Queue Saturation"
	CategoryPoolStarvation       FailureCategory = "Pool Starvation"
	CategoryCascadingFailure     FailureCategory = "Cascading Failure"
	CategoryAuthBottleneck       FailureCategory = "Auth Bottleneck"
	CategoryDBSaturation         FailureCategory = "DB Saturation"
	CategoryHiddenUpstream       FailureCategory = "Hidden Upstream Failure"
	CategoryTelemetryBlindSpot   FailureCategory = "Telemetry Blind Spot"
	CategoryTimeoutAmplification FailureCategory = "Timeout Amplification"
	CategoryDependencyStall      FailureCategory = "Dependency Stall"
	CategoryResourceThrashing    FailureCategory = "Resource Thrashing"
	CategoryEventLoopSaturation  FailureCategory = "Event Loop Saturation"
	CategoryLockContention       FailureCategory = "Lock Contention"
	CategoryBackpressureCollapse FailureCategory = "Backpressure Collapse"
	CategoryTrafficSurgeCollapse FailureCategory = "Traffic Surge Collapse"
)

type FailureEvidence struct {
	NodeID         string  `json:"node_id,omitempty"`
	Metric         string  `json:"metric"`
	Value          float64 `json:"value,omitempty"`
	Threshold      float64 `json:"threshold,omitempty"`
	Interpretation string  `json:"interpretation"`
}

type RemediationAction struct {
	Action         string  `json:"action"`
	Target         string  `json:"target,omitempty"`
	Priority       int     `json:"priority"`
	Rationale      string  `json:"rationale"`
	ExpectedEffect string  `json:"expected_effect"`
	Confidence     float64 `json:"confidence,omitempty"`
}

type FailureSemantics struct {
	Category         FailureCategory    `json:"category"`
	State            OperationalState   `json:"state"`
	Severity         float64            `json:"severity"`
	Confidence       float64            `json:"confidence"`
	RootCause        string             `json:"root_cause,omitempty"`
	Target           string             `json:"target,omitempty"`
	BlastRadius      int                `json:"blast_radius"`
	PropagationDepth int                `json:"propagation_depth"`
	Summary          string             `json:"summary"`
	Timeline         []string           `json:"timeline"`
	Evidence         []FailureEvidence  `json:"evidence"`
	Remediation      []RemediationAction `json:"remediation"`
	SafetyBlockers    []string           `json:"safety_blockers,omitempty"`
}

func BuildFailureSemantics(
	result *PipelineResult,
	target string,
	nodeStates map[string]phase3.NodeState,
) *FailureSemantics {
	if result == nil {
		return &FailureSemantics{
			Category: CategoryTelemetryBlindSpot,
			State:    StateWatch,
			Summary:  "Waiting for telemetry: no real pipeline result is available yet.",
		}
	}

	root := result.PrimaryRootCause()
	if root == "" {
		root = highestPressureNode(nodeStates)
	}
	if target == "" && result.Phase3Result != nil {
		target = result.Phase3Result.Target
	}

	rootState, hasRootState := nodeStates[root]
	if hasRootState {
		rootState = normalizedNodeState(rootState)
	}

	pressureNodes := pressureNodeIDs(nodeStates)
	blastRadius := len(pressureNodes)
	depth := propagationDepth(result)
	blockers := safetyBlockers(result)
	category := classifyFailureCategory(result, root, target, rootState, hasRootState, blastRadius, depth)
	state := classifyOperationalState(result, rootState, hasRootState, blastRadius, depth, category)
	confidence := semanticConfidence(result)
	severity := semanticSeverity(result, rootState, hasRootState, blastRadius, depth)

	sem := &FailureSemantics{
		Category:         category,
		State:            state,
		Severity:         severity,
		Confidence:       confidence,
		RootCause:        root,
		Target:           target,
		BlastRadius:      blastRadius,
		PropagationDepth: depth,
		SafetyBlockers:    blockers,
	}
	sem.Summary = buildSemanticSummary(result, sem, rootState, hasRootState)
	sem.Timeline = buildSemanticTimeline(result, sem, rootState, hasRootState)
	sem.Evidence = buildSemanticEvidence(result, sem, nodeStates, rootState, hasRootState)
	sem.Remediation = buildSemanticRemediation(result, sem, rootState, hasRootState)
	return sem
}

func classifyFailureCategory(
	result *PipelineResult,
	root, target string,
	rootState phase3.NodeState,
	hasRootState bool,
	blastRadius, depth int,
) FailureCategory {
	rootLower := strings.ToLower(root + " " + target)
	load := 0.0
	queue := 0.0
	if hasRootState {
		load = rootState.Load
		queue = rootState.QueueLength
	}

	if containsAny(rootLower, "auth", "oauth", "jwt", "token", "identity") && (load >= 0.85 || queue >= 100) {
		return CategoryAuthBottleneck
	}
	if containsAny(rootLower, "db", "database", "postgres", "mysql", "redis", "mongo", "proxy") && (load >= 0.85 || queue >= 100) {
		return CategoryDBSaturation
	}
	if depth >= 2 && blastRadius >= 2 {
		return CategoryCascadingFailure
	}
	if blastRadius >= 3 {
		return CategoryBackpressureCollapse
	}
	if hasRootState && queue >= 100 && load < 0.85 {
		return CategoryPoolStarvation
	}
	if hasRootState && (load >= 1.05 || queue >= 100) {
		return CategoryQueueSaturation
	}
	if result != nil && result.Phase2Dynamics != nil {
		switch result.Phase2Dynamics.Type {
		case phase2.OscillatingDynamics:
			return CategoryResourceThrashing
		case phase2.DivergingDynamics:
			if hasRootState && rootState.ArrivalRate > rootState.ServiceRate {
				if containsAny(rootLower, "gateway", "api", "edge", "ingress") && blastRadius >= 2 {
					return CategoryRetryStorm
				}
				return CategoryTrafficSurgeCollapse
			}
			return CategoryTimeoutAmplification
		case phase2.SaturatingDynamics:
			return CategoryQueueSaturation
		}
	}
	if result != nil && result.SafetyResult != nil {
		if result.SafetyResult.LatentRisk.Level.String() == "HIGH" {
			return CategoryHiddenUpstream
		}
		if result.SafetyResult.LatentRisk.GraphCoverage > 0 && result.SafetyResult.LatentRisk.GraphCoverage < 0.35 {
			return CategoryTelemetryBlindSpot
		}
	}
	if containsAny(rootLower, "lock", "mutex", "semaphore") {
		return CategoryLockContention
	}
	if containsAny(rootLower, "node", "event", "loop") && hasRootState && load >= 1.0 {
		return CategoryEventLoopSaturation
	}
	if hasRootState && rootState.ServiceRate > 0 && rootState.ArrivalRate == 0 && queue > 0 {
		return CategoryDependencyStall
	}
	return CategoryTelemetryBlindSpot
}

func classifyOperationalState(
	result *PipelineResult,
	rootState phase3.NodeState,
	hasRootState bool,
	blastRadius, depth int,
	category FailureCategory,
) OperationalState {
	maxLoad, maxQueue := maxPressure(result, rootState, hasRootState)
	if result != nil && result.Phase2Dynamics != nil && result.Phase2Dynamics.Type == phase2.ConvergingDynamics {
		if maxLoad < 1.05 && maxQueue < 100 {
			return StateRecovering
		}
	}
	if maxLoad >= 1.20 || maxQueue >= 10000 || blastRadius >= 4 {
		return StateCritical
	}
	if category == CategoryCascadingFailure || category == CategoryBackpressureCollapse {
		if blastRadius >= 3 {
			return StatePartialOutage
		}
		return StateCascadeRisk
	}
	if depth >= 2 || blastRadius >= 2 {
		return StateCascadeRisk
	}
	if result != nil && result.SafetyResult != nil && result.SafetyResult.Fallback.IsUnknown {
		return StateUnstable
	}
	if result != nil && result.Phase2Dynamics != nil {
		if result.Phase2Dynamics.Type == phase2.DivergingDynamics || result.Phase2Dynamics.Type == phase2.OscillatingDynamics {
			return StateUnstable
		}
	}
	if maxLoad >= 1.05 || maxQueue >= 100 {
		return StateDegraded
	}
	if maxLoad >= 0.60 || len(result.Phase2Patterns) > 0 {
		return StateWatch
	}
	return StateHealthy
}

func semanticConfidence(result *PipelineResult) float64 {
	if result == nil {
		return 0
	}
	if result.SafetyResult != nil {
		return round3(result.SafetyResult.Confidence.Score)
	}
	if result.Phase3Result != nil {
		return round3(result.Phase3Result.Confidence)
	}
	return 0
}

func semanticSeverity(
	result *PipelineResult,
	rootState phase3.NodeState,
	hasRootState bool,
	blastRadius, depth int,
) float64 {
	score := 0.0
	if hasRootState {
		score += math.Min(math.Max(rootState.Load-0.60, 0)/0.80, 0.45)
		score += math.Min(math.Log1p(math.Max(rootState.QueueLength, 0))/12.0, 0.30)
	}
	score += math.Min(float64(blastRadius)*0.08, 0.20)
	score += math.Min(float64(depth)*0.05, 0.10)
	if result != nil && result.SafetyResult != nil && result.SafetyResult.LatentRisk.Level.String() == "HIGH" {
		score += 0.15
	}
	if score > 1 {
		score = 1
	}
	return round3(score)
}

func buildSemanticSummary(
	result *PipelineResult,
	sem *FailureSemantics,
	rootState phase3.NodeState,
	hasRootState bool,
) string {
	if sem.RootCause == "" || !hasRootState {
		if len(sem.SafetyBlockers) > 0 {
			return "Runtime evidence is still insufficient for a clear operational diagnosis. Safety gate blockers: " + strings.Join(sem.SafetyBlockers, ", ") + "."
		}
		return "Waiting for enough real telemetry to identify what broke."
	}

	base := fmt.Sprintf("%s is the strongest observed failure point: arrival_rate %.2f, service_rate %.2f, rho %.2f, queue_length %.2f.",
		sem.RootCause, rootState.ArrivalRate, rootState.ServiceRate, rootState.Load, rootState.QueueLength)
	cause := fmt.Sprintf(" This maps to %s and the current system state is %s.", sem.Category, sem.State)
	propagation := ""
	if sem.PropagationDepth > 0 && sem.Target != "" && sem.Target != sem.RootCause {
		propagation = fmt.Sprintf(" The queue-pressure chain reaches %s across %d hop(s).", sem.Target, sem.PropagationDepth)
	} else if sem.BlastRadius > 1 {
		propagation = fmt.Sprintf(" %d services are under pressure, so cascade risk is elevated.", sem.BlastRadius)
	}
	safety := ""
	if result != nil && result.SafetyResult != nil && result.SafetyResult.Fallback.IsUnknown {
		safety = " Automated remediation is blocked because the safety gate does not trust the current causal structure."
	}
	return base + cause + propagation + safety
}

func buildSemanticTimeline(
	result *PipelineResult,
	sem *FailureSemantics,
	rootState phase3.NodeState,
	hasRootState bool,
) []string {
	lines := make([]string, 0, 6)
	if sem.RootCause != "" && hasRootState {
		lines = append(lines, fmt.Sprintf("%s: runtime pressure formed at rho %.2f with queue_length %.2f.", sem.RootCause, rootState.Load, rootState.QueueLength))
	}
	if result != nil && len(result.PhysicsRootCauses) > 0 {
		chain := result.PhysicsRootCauses[0].PropagationChain
		if chain != nil && len(chain.Hops) > 0 {
			for _, hop := range chain.Hops {
				lines = append(lines, fmt.Sprintf("%s: effective arrival %.2f vs service %.2f, delay %.2fs, strength %.2f.",
					hop.NodeID, hop.ArrivalRate, hop.ServiceRate, hop.GeneratedDelay, hop.CausalStrength))
			}
		}
	}
	if len(lines) == 0 && len(sem.SafetyBlockers) > 0 {
		lines = append(lines, "Safety gate blocked root-cause assertion: "+strings.Join(sem.SafetyBlockers, ", ")+".")
	}
	if sem.Target != "" && sem.Target != sem.RootCause {
		lines = append(lines, fmt.Sprintf("%s: selected analysis target affected by the current causal graph.", sem.Target))
	}
	return lines
}

func buildSemanticEvidence(
	result *PipelineResult,
	sem *FailureSemantics,
	nodeStates map[string]phase3.NodeState,
	rootState phase3.NodeState,
	hasRootState bool,
) []FailureEvidence {
	evidence := make([]FailureEvidence, 0, 10)
	if hasRootState {
		evidence = append(evidence,
			FailureEvidence{NodeID: sem.RootCause, Metric: "rho", Value: round3(rootState.Load), Threshold: 0.85, Interpretation: "queue utilisation is at or above pressure threshold"},
			FailureEvidence{NodeID: sem.RootCause, Metric: "arrival_rate", Value: round3(rootState.ArrivalRate), Interpretation: "observed incoming work rate"},
			FailureEvidence{NodeID: sem.RootCause, Metric: "service_rate", Value: round3(rootState.ServiceRate), Interpretation: "observed processing capacity"},
			FailureEvidence{NodeID: sem.RootCause, Metric: "queue_length", Value: round3(rootState.QueueLength), Threshold: 100, Interpretation: "backlog depth from runtime telemetry"},
		)
	}
	for _, nodeID := range pressureNodeIDs(nodeStates) {
		if nodeID == sem.RootCause {
			continue
		}
		st := normalizedNodeState(nodeStates[nodeID])
		evidence = append(evidence, FailureEvidence{
			NodeID:         nodeID,
			Metric:         "rho",
			Value:          round3(st.Load),
			Threshold:      0.85,
			Interpretation: "additional node under pressure in the same observation window",
		})
	}
	if result != nil && result.Phase2Dynamics != nil {
		evidence = append(evidence, FailureEvidence{
			Metric:         "dynamics",
			Value:          round3(result.Phase2Dynamics.DivergenceRate),
			Interpretation: fmt.Sprintf("system dynamics classified as %s", result.Phase2Dynamics.Type),
		})
	}
	if result != nil {
		evidence = append(evidence, FailureEvidence{
			Metric:         "patterns_detected",
			Value:          float64(len(result.Phase2Patterns)),
			Interpretation: "phase 2 pattern detections in the real telemetry window",
		})
	}
	if result != nil && result.SafetyResult != nil {
		evidence = append(evidence,
			FailureEvidence{Metric: "graph_coverage", Value: round3(result.SafetyResult.LatentRisk.GraphCoverage), Threshold: 0.50, Interpretation: "fraction of graph nodes covered by identified causal paths"},
			FailureEvidence{Metric: "determinism", Value: round3(result.SafetyResult.Confidence.Components.Determinism), Threshold: 0.45, Interpretation: "ranking stability used by the confidence engine"},
		)
	}
	return evidence
}

func buildSemanticRemediation(
	result *PipelineResult,
	sem *FailureSemantics,
	rootState phase3.NodeState,
	hasRootState bool,
) []RemediationAction {
	if result != nil && result.SafetyResult != nil && result.SafetyResult.Fallback.IsUnknown {
		return fallbackRemediation(result, sem)
	}

	target := sem.RootCause
	if target == "" {
		target = sem.Target
	}
	conf := sem.Confidence
	actions := make([]RemediationAction, 0, 4)
	add := func(action, rationale, effect string) {
		actions = append(actions, RemediationAction{
			Action:         action,
			Target:         target,
			Priority:       len(actions) + 1,
			Rationale:      rationale,
			ExpectedEffect: effect,
			Confidence:     conf,
		})
	}

	switch sem.Category {
	case CategoryAuthBottleneck:
		add("scale auth-service capacity", "auth path is the strongest observed pressure point", "raises service_rate and reduces authentication queueing")
		add("throttle auth retry fanout", "retry amplification can overload auth dependencies during latency spikes", "reduces arrival_rate into the bottleneck")
		add("enable token-cache or degraded auth mode", "serves repeat validation locally while upstream auth recovers", "shrinks request latency and dependency pressure")
	case CategoryDBSaturation:
		add("scale db-proxy replicas", "database proxy shows queue pressure or saturation", "adds processing capacity and reduces queue_length")
		add("increase connection pool within safe database limits", "pool starvation can hold requests even when CPU is not saturated", "reduces waiting time at the proxy")
		add("shed low-priority database work", "protects critical paths while backlog drains", "lowers arrival_rate until rho returns below 1")
	case CategoryCascadingFailure, CategoryBackpressureCollapse:
		add("apply circuit breaker at the upstream caller", "multiple services are under pressure in the same causal window", "prevents further propagation")
		add("throttle ingress to the saturated path", "arrival_rate is exceeding safe utilisation", "reduces queue growth and timeout amplification")
		add("isolate the failing dependency", "cascade risk is elevated", "keeps unaffected services from joining the failure chain")
	case CategoryPoolStarvation:
		add("increase pool size or worker concurrency cautiously", "backlog is high while current utilisation is below saturation", "releases queued work without overloading downstream capacity")
		add("audit blocked connections or worker leases", "queue growth without matching rho can indicate pool starvation", "restores effective service_rate")
	case CategoryQueueSaturation, CategoryTrafficSurgeCollapse, CategoryRetryStorm, CategoryTimeoutAmplification:
		add("reduce upstream concurrency", "arrival_rate is at or above service capacity", "brings rho back below saturation")
		add("drain queues before raising retry budgets", "existing backlog is operational evidence of pressure", "reduces timeout amplification")
		add("scale the saturated service", "capacity is below observed demand", "increases service_rate for the hot path")
	case CategoryResourceThrashing:
		add("stabilize workload concurrency", "oscillating dynamics indicate repeated over-correction", "reduces utilisation swings")
		add("pinpoint noisy workloads on the service", "thrashing consumes capacity without stable throughput", "restores predictable service_rate")
	default:
		add("collect targeted telemetry before remediation", "the current evidence does not support a safe operational action", "improves graph coverage and confidence")
	}

	if hasRootState && rootState.Load >= 1.20 {
		add("shed non-critical traffic", "rho is materially above 1.0", "stops unbounded queue growth while capacity recovers")
	}
	return actions
}

func fallbackRemediation(result *PipelineResult, sem *FailureSemantics) []RemediationAction {
	actions := make([]RemediationAction, 0, 4)
	target := sem.RootCause
	if target == "" {
		target = sem.Target
	}
	add := func(action, rationale, effect string) {
		actions = append(actions, RemediationAction{
			Action:         action,
			Target:         target,
			Priority:       len(actions) + 1,
			Rationale:      rationale,
			ExpectedEffect: effect,
			Confidence:     sem.Confidence,
		})
	}
	add("pause automated remediation", "safety gate returned UNKNOWN", "prevents acting on an untrusted causal assignment")
	if result != nil && result.SafetyResult != nil {
		for _, probe := range result.SafetyResult.Fallback.ProbeRecommendations {
			actions = append(actions, RemediationAction{
				Action:         "collect " + probe.Metric,
				Target:         probe.NodeID,
				Priority:       len(actions) + 1,
				Rationale:      probe.Rationale,
				ExpectedEffect: "improves observability for the blocked causal path",
				Confidence:     sem.Confidence,
			})
		}
	}
	if len(actions) == 1 {
		add("instrument upstream dependency latency", "hidden upstream failure or telemetry blind spot is plausible", "separates true dependency delay from local saturation")
	}
	return actions
}

func highestPressureNode(nodeStates map[string]phase3.NodeState) string {
	best := ""
	bestScore := -1.0
	for _, nodeID := range sortedKeys(nodeStates) {
		st := normalizedNodeState(nodeStates[nodeID])
		score := st.Load + math.Min(math.Log1p(math.Max(st.QueueLength, 0))/10.0, 2.0)
		if score > bestScore {
			best = nodeID
			bestScore = score
		}
	}
	return best
}

func pressureNodeIDs(nodeStates map[string]phase3.NodeState) []string {
	ids := make([]string, 0)
	for nodeID, st := range nodeStates {
		st = normalizedNodeState(st)
		if st.Load >= 0.85 || st.QueueLength >= 100 {
			ids = append(ids, nodeID)
		}
	}
	sort.Strings(ids)
	return ids
}

func propagationDepth(result *PipelineResult) int {
	if result == nil || len(result.PhysicsRootCauses) == 0 {
		return 0
	}
	chain := result.PhysicsRootCauses[0].PropagationChain
	if chain == nil {
		return 0
	}
	return len(chain.Hops)
}

func safetyBlockers(result *PipelineResult) []string {
	if result == nil || result.SafetyResult == nil || !result.SafetyResult.Fallback.IsUnknown {
		return nil
	}
	out := make([]string, 0, len(result.SafetyResult.Fallback.Reasons))
	for _, reason := range result.SafetyResult.Fallback.Reasons {
		out = append(out, reason.String())
	}
	return out
}

func maxPressure(result *PipelineResult, rootState phase3.NodeState, hasRootState bool) (float64, float64) {
	maxLoad := 0.0
	maxQueue := 0.0
	if hasRootState {
		maxLoad = rootState.Load
		maxQueue = rootState.QueueLength
	}
	if result != nil {
		for _, rc := range result.PhysicsRootCauses {
			if rc.Load > maxLoad {
				maxLoad = rc.Load
			}
			if rc.QueueLength > maxQueue {
				maxQueue = rc.QueueLength
			}
		}
	}
	return maxLoad, maxQueue
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

// ============================================================================
// CONFIDENCE NARRATIVE — plain-English explanation of confidence components
// ============================================================================

// ConfidenceNarrative converts raw confidence components into a human-readable
// explanation that an SRE can act on, rather than a numeric score alone.
func ConfidenceNarrative(score float64, determinism, graphCoverage, residualExplained, roleConsistency float64, latentRisk, fallbackReason string) string {
	if latentRisk == "HIGH" {
		if graphCoverage < 0.25 {
			return "Confidence is blocked because fewer than 25% of observed infrastructure nodes appear in the causal model. The majority of the system is invisible to the causal engine — a hidden service is almost certainly influencing the failure."
		}
		if determinism < 0.3 {
			return "Confidence is blocked because the dominant root-cause changed between inference passes. The causal graph is actively unstable, which means the system state is still evolving or multiple competing failure modes are present simultaneously."
		}
		return "Confidence is blocked because a hidden variable is likely influencing the system. Latency or load increased without corresponding saturation on all known infrastructure nodes, suggesting an unmonitored upstream dependency."
	}
	if score >= 0.75 {
		return "High confidence: causal paths are stable, graph coverage is adequate, and multiple evidence channels corroborate the same root cause. Automated remediation is permissible."
	}
	reasons := make([]string, 0, 4)
	if determinism < 0.5 {
		reasons = append(reasons, "the root-cause ranking changed between consecutive inference cycles — the system state may still be evolving")
	}
	if graphCoverage < 0.5 {
		pct := int(graphCoverage * 100)
		reasons = append(reasons, fmt.Sprintf("only %d%% of observed nodes lie on identified causal paths — the graph model does not fully represent the running system", pct))
	}
	if residualExplained < 0.4 {
		reasons = append(reasons, "the majority of observed signal variation is not accounted for by any identified causal path — unexplained variance suggests a missing dependency or hidden load spike")
	}
	if roleConsistency < 0.5 {
		reasons = append(reasons, "the fusion layer and the explanation layer disagree on which nodes are causal actors — structural inconsistency reduces trust in the root-cause assignment")
	}
	if len(reasons) == 0 {
		return fmt.Sprintf("Moderate confidence (%.0f%%). Evidence is partially consistent but carries material uncertainty. Human review is recommended before automated intervention.", score*100)
	}
	msg := fmt.Sprintf("Confidence is reduced to %.0f%% because: ", score*100)
	for i, r := range reasons {
		if i > 0 {
			msg += "; and "
		}
		msg += r
	}
	msg += "."
	return msg
}

// IncidentTitle generates a concise, operator-facing incident title.
func IncidentTitle(sem *FailureSemantics) string {
	if sem == nil {
		return "System Status Indeterminate"
	}
	switch sem.State {
	case StateHealthy:
		return "System Healthy — No Active Incident"
	case StateWatch:
		return fmt.Sprintf("Watch: %s showing early pressure signals", firstNode(sem.RootCause, sem.Target))
	case StateRecovering:
		return fmt.Sprintf("Recovering: %s pressure subsiding", firstNode(sem.RootCause, sem.Target))
	}
	root := sem.RootCause
	if root == "" {
		root = sem.Target
	}
	switch sem.Category {
	case CategoryRetryStorm:
		return fmt.Sprintf("Retry Storm: %s amplifying request pressure across %d services", root, sem.BlastRadius)
	case CategoryQueueSaturation:
		return fmt.Sprintf("Queue Saturation: %s queue backlog exceeds capacity", root)
	case CategoryPoolStarvation:
		return fmt.Sprintf("Pool Starvation: %s connection pool exhausted with high backlog", root)
	case CategoryCascadingFailure:
		return fmt.Sprintf("Cascading Failure: %d services under pressure — chain started at %s", sem.BlastRadius, root)
	case CategoryAuthBottleneck:
		return fmt.Sprintf("Auth Bottleneck: authentication path at %s saturated", root)
	case CategoryDBSaturation:
		return fmt.Sprintf("Database Saturation: %s database layer at or above capacity", root)
	case CategoryHiddenUpstream:
		return fmt.Sprintf("Hidden Upstream Failure: unmonitored dependency influencing %s", firstNode(sem.Target, root))
	case CategoryTelemetryBlindSpot:
		return "Telemetry Blind Spot: insufficient observability for root-cause assertion"
	case CategoryTimeoutAmplification:
		return fmt.Sprintf("Timeout Amplification: %s generating cascading timeout pressure", root)
	case CategoryDependencyStall:
		return fmt.Sprintf("Dependency Stall: %s stalled — queue growing without arrival rate", root)
	case CategoryResourceThrashing:
		return fmt.Sprintf("Resource Thrashing: %s workload oscillating — utilisation unstable", root)
	case CategoryEventLoopSaturation:
		return fmt.Sprintf("Event Loop Saturation: %s event loop at 100%% utilisation", root)
	case CategoryLockContention:
		return fmt.Sprintf("Lock Contention: %s blocking on serialised resource", root)
	case CategoryBackpressureCollapse:
		return fmt.Sprintf("Backpressure Collapse: %d services pushing back — system-wide pressure", sem.BlastRadius)
	case CategoryTrafficSurgeCollapse:
		return fmt.Sprintf("Traffic Surge: %s overwhelmed by sudden arrival rate increase", root)
	}
	return fmt.Sprintf("%s: %s under pressure (%s)", sem.State, root, sem.Category)
}

// OperationalNarrative generates a full SRE-style incident narrative from the pipeline result.
// The output reads like a senior engineer describing what is happening and why.
func OperationalNarrative(sem *FailureSemantics) []string {
	if sem == nil {
		return []string{"No pipeline result available. Waiting for telemetry."}
	}
	lines := make([]string, 0, 8)

	// Opening state
	switch sem.State {
	case StateHealthy:
		lines = append(lines, "All observed services are operating within normal parameters.")
		return lines
	case StateWatch:
		lines = append(lines, fmt.Sprintf("%s is showing early signs of pressure but has not crossed failure thresholds.", firstNode(sem.RootCause, sem.Target)))
	case StateRecovering:
		lines = append(lines, fmt.Sprintf("The system is recovering. %s pressure is subsiding — dynamics are converging toward stable state.", firstNode(sem.RootCause, sem.Target)))
		return lines
	default:
		lines = append(lines, fmt.Sprintf("An active %s has been detected.", strings.ToLower(string(sem.Category))))
	}

	// Root cause
	if sem.RootCause != "" {
		root := sem.RootCause
		for _, ev := range sem.Evidence {
			if ev.NodeID == root && ev.Metric == "rho" {
				lines = append(lines, fmt.Sprintf("%s is the strongest observed failure point with a utilisation ratio (ρ) of %.2f — a value above 1.0 means arrivals exceed service capacity and the queue grows unboundedly.", root, ev.Value))
				break
			}
		}
	}

	// Timeline / propagation
	if len(sem.Timeline) > 0 {
		lines = append(lines, "Causal chain:")
		for _, step := range sem.Timeline {
			lines = append(lines, "  → "+step)
		}
	}

	// Blast radius
	if sem.BlastRadius > 1 {
		lines = append(lines, fmt.Sprintf("%d services are currently under pressure in the same observation window, elevating cascade risk.", sem.BlastRadius))
	}

	// Hidden variable assessment
	if sem.Category == CategoryHiddenUpstream {
		lines = append(lines, "A hidden upstream dependency is the most probable explanation: latency increased without corresponding CPU, memory, or network saturation on any monitored node. The failing service is not directly observable.")
	}
	if sem.Category == CategoryTelemetryBlindSpot {
		lines = append(lines, "Telemetry coverage is insufficient to safely assert a root cause. The causal model covers less than half the observable system. Instrumentation gaps must be closed before automated remediation is safe.")
	}

	// Safety gate
	if len(sem.SafetyBlockers) > 0 {
		lines = append(lines, fmt.Sprintf("Automated remediation is blocked: %s.", strings.Join(sem.SafetyBlockers, "; ")))
	}

	return lines
}

// ExploreQuestion answers a natural-language operator question using deterministic
// pattern matching over the failure semantics and pipeline result.
// No LLM. No external calls. Pure signal → language translation.
func ExploreQuestion(question string, sem *FailureSemantics, confidenceNarrative, incidentTitle string) ExploreAnswer {
	q := strings.ToLower(strings.TrimSpace(question))

	// Why / root cause
	if containsAny(q, "why", "cause", "root", "origin", "start") {
		narrative := OperationalNarrative(sem)
		return ExploreAnswer{
			Question: question,
			Answer:   strings.Join(narrative, "\n"),
			Category: "root_cause",
			Evidence: sem.Evidence,
			Actions:  sem.Remediation,
		}
	}

	// Confidence
	if containsAny(q, "confidence", "trust", "certain", "sure", "determinism", "coverage") {
		return ExploreAnswer{
			Question: question,
			Answer:   confidenceNarrative,
			Category: "confidence",
			Evidence: sem.Evidence,
		}
	}

	// What to do / remediation
	if containsAny(q, "do", "fix", "remediat", "action", "resolve", "mitigat", "help") {
		if len(sem.Remediation) == 0 {
			return ExploreAnswer{
				Question: question,
				Answer:   "No specific remediation is available yet — the system is still building enough telemetry to make a confident assertion. Continue monitoring.",
				Category: "remediation",
			}
		}
		actions := make([]string, 0, len(sem.Remediation))
		for _, r := range sem.Remediation {
			actions = append(actions, fmt.Sprintf("[%d] %s — %s (expected: %s)", r.Priority, r.Action, r.Rationale, r.ExpectedEffect))
		}
		return ExploreAnswer{
			Question: question,
			Answer:   "Recommended actions in priority order:\n" + strings.Join(actions, "\n"),
			Category: "remediation",
			Actions:  sem.Remediation,
		}
	}

	// Retry storm / retries
	if containsAny(q, "retry", "storm", "amplif", "fanout") {
		if sem.Category == CategoryRetryStorm {
			return ExploreAnswer{
				Question: question,
				Answer:   fmt.Sprintf("A retry storm is active. %s is generating excessive retry traffic that is amplifying load across %d downstream services. The arrival rate exceeds service capacity, causing queues to grow. Each timeout triggers additional retries, compounding the overload.", sem.RootCause, sem.BlastRadius),
				Category: "retry_storm",
				Evidence: sem.Evidence,
				Actions:  sem.Remediation,
			}
		}
		return ExploreAnswer{
			Question: question,
			Answer:   fmt.Sprintf("No retry storm pattern detected. Current failure category is %s. Retry amplification is not the dominant signal in the current telemetry window.", sem.Category),
			Category: "retry_storm",
			Evidence: sem.Evidence,
		}
	}

	// Hidden / latent / invisible
	if containsAny(q, "hidden", "latent", "invisible", "unknown", "mystery", "upstream") {
		if sem.Category == CategoryHiddenUpstream {
			return ExploreAnswer{
				Question: question,
				Answer:   "A hidden upstream failure is the most probable explanation. The causal engine detected latency and load increase in monitored services without corresponding infrastructure saturation (CPU, memory, network) on any known node. This pattern is the canonical signature of an unmonitored upstream dependency failing silently. The failing service is not in the current causal graph.",
				Category: "latent_variable",
				Evidence: sem.Evidence,
			}
		}
		return ExploreAnswer{
			Question: question,
			Answer:   fmt.Sprintf("No strong hidden-variable signal detected in the current pass. The current failure pattern is classified as %s. Latent risk is assessed based on graph coverage and residual unexplained signal — check the confidence panel for current levels.", sem.Category),
			Category: "latent_variable",
			Evidence: sem.Evidence,
		}
	}

	// What changed / dynamics
	if containsAny(q, "changed", "change", "different", "dynamic", "spike", "spike", "surge") {
		return ExploreAnswer{
			Question: question,
			Answer:   fmt.Sprintf("Current system state: %s. Failure pattern: %s. Blast radius: %d service(s) affected. Propagation depth: %d causal hops from root to target. The incident title is: %s", sem.State, sem.Category, sem.BlastRadius, sem.PropagationDepth, incidentTitle),
			Category: "dynamics",
			Evidence: sem.Evidence,
		}
	}

	// Evidence / metrics
	if containsAny(q, "evidence", "metric", "data", "signal", "telemetry", "number") {
		if len(sem.Evidence) == 0 {
			return ExploreAnswer{
				Question: question,
				Answer:   "No evidence available yet. The system needs at least 4 samples per node (~32 seconds) before causal analysis can begin.",
				Category: "evidence",
			}
		}
		parts := make([]string, 0, len(sem.Evidence))
		for _, ev := range sem.Evidence {
			node := ev.NodeID
			if node == "" {
				node = "system"
			}
			parts = append(parts, fmt.Sprintf("%s/%s = %.3f (threshold: %.3f) — %s", node, ev.Metric, ev.Value, ev.Threshold, ev.Interpretation))
		}
		return ExploreAnswer{
			Question: question,
			Answer:   "Current operational evidence:\n" + strings.Join(parts, "\n"),
			Category: "evidence",
			Evidence: sem.Evidence,
		}
	}

	// Service / which service
	if containsAny(q, "service", "which", "who", "node", "container") {
		root := sem.RootCause
		if root == "" {
			root = sem.Target
		}
		if root == "" {
			return ExploreAnswer{
				Question: question,
				Answer:   "No specific service has been identified as the root cause yet. The causal engine needs more telemetry samples or the system may be healthy.",
				Category: "service",
			}
		}
		return ExploreAnswer{
			Question: question,
			Answer:   fmt.Sprintf("The identified root cause is: %s. Current failure classification: %s. System state: %s. %d service(s) are affected in total.", root, sem.Category, sem.State, sem.BlastRadius),
			Category: "service",
			Evidence: sem.Evidence,
		}
	}

	// Default — summarise state
	narrative := OperationalNarrative(sem)
	return ExploreAnswer{
		Question: question,
		Answer:   fmt.Sprintf("Current status: %s\n\n%s", incidentTitle, strings.Join(narrative, "\n")),
		Category: "summary",
		Evidence: sem.Evidence,
		Actions:  sem.Remediation,
	}
}

// ExploreAnswer is the structured response from ExploreQuestion.
type ExploreAnswer struct {
	Question string            `json:"question"`
	Answer   string            `json:"answer"`
	Category string            `json:"category"`
	Evidence []FailureEvidence  `json:"evidence,omitempty"`
	Actions  []RemediationAction `json:"actions,omitempty"`
}

func firstNode(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
