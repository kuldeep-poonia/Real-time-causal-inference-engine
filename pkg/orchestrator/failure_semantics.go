package orchestrator

import (
	"fmt"
	"math"
	"sort"
	"strings"

	phase2 "absia/internal/intelligence/phase2_pattern"
	phase3 "absia/internal/intelligence/phase3_causal"
	"absia/pkg/adaptive"
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
	Category         FailureCategory     `json:"category"`
	State            OperationalState    `json:"state"`
	Severity         float64             `json:"severity"`
	Confidence       float64             `json:"confidence"`
	RootCause        string              `json:"root_cause,omitempty"`
	Target           string              `json:"target,omitempty"`
	BlastRadius      int                 `json:"blast_radius"`
	PropagationDepth int                 `json:"propagation_depth"`
	Summary          string              `json:"summary"`
	Timeline         []string            `json:"timeline"`
	Evidence         []FailureEvidence   `json:"evidence"`
	Remediation      []RemediationAction `json:"remediation"`
	SafetyBlockers   []string            `json:"safety_blockers,omitempty"`
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
			Summary:  "Waiting for service data: no real pipeline result is available yet.",
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
		SafetyBlockers:   blockers,
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
	
	var loadIsAnomaly, queueIsAnomaly, loadIsExtreme bool
	if hasRootState {
		profile := adaptive.GlobalStore.GetProfile(root)
		loadIsAnomaly = profile.EvaluateLoad(rootState.Load).IsAnomaly
		queueIsAnomaly = profile.EvaluateQueue(rootState.QueueLength).IsAnomaly
		// For extreme cases like EventLoopSaturation
		loadIsExtreme = rootState.Load >= (profile.EvaluateLoad(rootState.Load).Value * 1.2)
	}

	if containsAny(rootLower, "auth", "oauth", "jwt", "token", "identity") && (loadIsAnomaly || queueIsAnomaly) {
		return CategoryAuthBottleneck
	}
	if containsAny(rootLower, "db", "database", "postgres", "mysql", "redis", "mongo", "proxy") && (loadIsAnomaly || queueIsAnomaly) {
		return CategoryDBSaturation
	}
	if depth >= 2 && blastRadius >= 2 {
		return CategoryCascadingFailure
	}
	if blastRadius >= 3 {
		return CategoryBackpressureCollapse
	}
	if hasRootState && queueIsAnomaly && !loadIsAnomaly {
		return CategoryPoolStarvation
	}
	if hasRootState && (loadIsAnomaly || queueIsAnomaly) {
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
		if result.SafetyResult.LatentRisk.PosteriorVariance > 0.25 {
			return CategoryTelemetryBlindSpot
		}
	}
	if containsAny(rootLower, "lock", "mutex", "semaphore") {
		return CategoryLockContention
	}
	if containsAny(rootLower, "node", "event", "loop") && hasRootState && loadIsExtreme {
		return CategoryEventLoopSaturation
	}
	if hasRootState && rootState.ServiceRate > 0 && rootState.ArrivalRate == 0 && queueIsAnomaly {
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
	var loadIsAnomaly, queueIsAnomaly, maxLoadIsExtreme bool
	
	if hasRootState {
		// Because maxPressure looks at root causes, we'll approximate with root node profile
		// for simplicity, or we can check the worst anomaly. Let's use root profile for max load.
		maxLoad, maxQueue := maxPressure(result, rootState, hasRootState)
		profile := adaptive.GlobalStore.GetProfile(result.PrimaryRootCause()) // Default to root
		
		loadRes := profile.EvaluateLoad(maxLoad)
		queueRes := profile.EvaluateQueue(maxQueue)
		loadIsAnomaly = loadRes.IsAnomaly
		queueIsAnomaly = queueRes.IsAnomaly
		maxLoadIsExtreme = maxLoad >= (loadRes.Value * 1.2) // Approximating maxLoad >= 1.20
	}

	if result != nil && result.Phase2Dynamics != nil && result.Phase2Dynamics.Type == phase2.ConvergingDynamics {
		if !loadIsAnomaly && !queueIsAnomaly {
			return StateRecovering
		}
	}
	if maxLoadIsExtreme || blastRadius >= 4 { // maxQueue >= 10000 removed, replaced by maxLoad extreme
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
	if loadIsAnomaly || queueIsAnomaly {
		return StateDegraded
	}
	if hasRootState && (rootState.Load >= 0.60 || len(result.Phase2Patterns) > 0) { // Keep 0.60 for watch? Or replace. Let's use 60% of threshold
		profile := adaptive.GlobalStore.GetProfile(result.PrimaryRootCause())
		if rootState.Load >= (profile.EvaluateLoad(rootState.Load).Value * 0.7) || len(result.Phase2Patterns) > 0 {
			return StateWatch
		}
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
			return "ABSIA does not have enough proof for one clear cause yet. More data is needed because: " + strings.Join(sem.SafetyBlockers, ", ") + "."
		}
		return "Waiting for enough real service data to identify what happened."
	}

	base := fmt.Sprintf("%s is the service most likely starting the issue: incoming work %.2f, capacity %.2f, load %.2f, waiting work %.2f.",
		sem.RootCause, rootState.ArrivalRate, rootState.ServiceRate, rootState.Load, rootState.QueueLength)
	cause := fmt.Sprintf(" This looks like %s. Current system state: %s.", sem.Category, sem.State)
	propagation := ""
	if sem.PropagationDepth > 0 && sem.Target != "" && sem.Target != sem.RootCause {
		propagation = fmt.Sprintf(" Pressure appears to reach %s across %d step(s).", sem.Target, sem.PropagationDepth)
	} else if sem.BlastRadius > 1 {
		propagation = fmt.Sprintf(" %d services are under pressure, so cascade risk is elevated.", sem.BlastRadius)
	}
	safety := ""
	if result != nil && result.SafetyResult != nil && result.SafetyResult.Fallback.IsUnknown {
		safety = " Automated fixes are paused because ABSIA is not sure enough yet."
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
		lines = append(lines, fmt.Sprintf("%s: pressure started here; load is %.2f and waiting work is %.2f.", sem.RootCause, rootState.Load, rootState.QueueLength))
	}
	if result != nil && len(result.PhysicsRootCauses) > 0 {
		chain := result.PhysicsRootCauses[0].PropagationChain
		if chain != nil && len(chain.Hops) > 0 {
			for _, hop := range chain.Hops {
				lines = append(lines, fmt.Sprintf("%s: incoming work %.2f vs capacity %.2f; expected delay %.2fs.",
					hop.NodeID, hop.ArrivalRate, hop.ServiceRate, hop.GeneratedDelay))
			}
		}
	}
	if len(lines) == 0 && len(sem.SafetyBlockers) > 0 {
		lines = append(lines, "More proof is needed before naming one cause: "+strings.Join(sem.SafetyBlockers, ", ")+".")
	}
	if sem.Target != "" && sem.Target != sem.RootCause {
		lines = append(lines, fmt.Sprintf("%s: selected service may be affected by this issue.", sem.Target))
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
			FailureEvidence{NodeID: sem.RootCause, Metric: "load", Value: round3(rootState.Load), Threshold: 0.85, Interpretation: "load is at or above the pressure threshold"},
			FailureEvidence{NodeID: sem.RootCause, Metric: "incoming_work", Value: round3(rootState.ArrivalRate), Interpretation: "incoming work observed for this service"},
			FailureEvidence{NodeID: sem.RootCause, Metric: "capacity", Value: round3(rootState.ServiceRate), Interpretation: "processing capacity observed for this service"},
			FailureEvidence{NodeID: sem.RootCause, Metric: "waiting_work", Value: round3(rootState.QueueLength), Interpretation: "waiting work reported by service data"},
		)
	}
	for _, nodeID := range pressureNodeIDs(nodeStates) {
		if nodeID == sem.RootCause {
			continue
		}
		st := normalizedNodeState(nodeStates[nodeID])
		evidence = append(evidence, FailureEvidence{
			NodeID:         nodeID,
			Metric:         "load",
			Value:          round3(st.Load),
			Interpretation: "another service needs attention at the same time",
		})
	}
	if result != nil && result.Phase2Dynamics != nil {
		evidence = append(evidence, FailureEvidence{
			Metric:         "dynamics",
			Value:          round3(result.Phase2Dynamics.DivergenceRate),
			Interpretation: fmt.Sprintf("recent service behavior is classified as %s", result.Phase2Dynamics.Type),
		})
	}
	if result != nil {
		evidence = append(evidence, FailureEvidence{
			Metric:         "patterns_detected",
			Value:          float64(len(result.Phase2Patterns)),
			Interpretation: "patterns found in recent service data",
		})
	}
	if result != nil && result.SafetyResult != nil {
		evidence = append(evidence,
			FailureEvidence{Metric: "bayesian_posterior_variance", Value: round3(result.SafetyResult.LatentRisk.PosteriorVariance), Threshold: 0.25, Interpretation: "ratio of uncertainty variance to squared effect"},
			FailureEvidence{Metric: "consistency", Value: round3(result.SafetyResult.Confidence.Components.Determinism), Threshold: 0.45, Interpretation: "how consistent the likely cause is between checks"},
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
		add("add capacity to auth-service", "auth is the strongest observed pressure point", "raises capacity and reduces authentication waiting")
		add("slow down repeated auth retries", "retry bursts can overload auth dependencies during latency spikes", "reduces incoming work into the bottleneck")
		add("enable token-cache or degraded auth mode", "serves repeat validation locally while upstream auth recovers", "shrinks request latency and dependency pressure")
	case CategoryDBSaturation:
		add("add db-proxy replicas", "database proxy shows waiting work or saturation", "adds processing capacity and reduces waiting work")
		add("increase connection pool within safe database limits", "pool starvation can hold requests even when CPU is not saturated", "reduces waiting time at the proxy")
		add("pause low-priority database work", "protects critical paths while backlog drains", "lowers incoming work until load returns below saturation")
	case CategoryCascadingFailure, CategoryBackpressureCollapse:
		add("apply circuit breaker at the upstream caller", "multiple services are under pressure in the same causal window", "prevents further propagation")
		add("slow incoming traffic to the saturated path", "incoming work is exceeding safe capacity", "reduces queue growth and timeout amplification")
		add("isolate the failing dependency", "cascade risk is elevated", "keeps unaffected services from joining the failure chain")
	case CategoryPoolStarvation:
		add("increase pool size or worker concurrency cautiously", "waiting work is high while current load is below saturation", "releases queued work without overloading downstream capacity")
		add("check blocked connections or worker leases", "waiting work can build when workers are stuck", "restores effective capacity")
	case CategoryQueueSaturation, CategoryTrafficSurgeCollapse, CategoryRetryStorm, CategoryTimeoutAmplification:
		add("reduce upstream concurrency", "incoming work is at or above service capacity", "brings load back below saturation")
		add("drain queues before raising retry budgets", "existing backlog is operational evidence of pressure", "reduces timeout amplification")
		add("add capacity to the saturated service", "capacity is below observed demand", "increases capacity for the hot path")
	case CategoryResourceThrashing:
		add("stabilize workload concurrency", "oscillating dynamics indicate repeated over-correction", "reduces utilisation swings")
		add("pinpoint noisy workloads on the service", "thrashing consumes capacity without stable throughput", "restores predictable capacity")
	default:
		add("collect targeted service data before acting", "the current evidence does not support a safe operational action", "improves coverage and confidence")
	}

	if hasRootState && rootState.Load >= 1.20 {
		add("shed non-critical traffic", "load is materially above safe capacity", "stops unbounded queue growth while capacity recovers")
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
	add("pause automated fixes", "ABSIA is not sure enough yet", "prevents unsafe automated action")
	if result != nil && result.SafetyResult != nil {
		for _, probe := range result.SafetyResult.Fallback.ProbeRecommendations {
			actions = append(actions, RemediationAction{
				Action:         "collect " + probe.Metric,
				Target:         probe.NodeID,
				Priority:       len(actions) + 1,
				Rationale:      probe.Rationale,
				ExpectedEffect: "adds the missing data needed for a safer answer",
				Confidence:     sem.Confidence,
			})
		}
	}
	if len(actions) == 1 {
		add("measure upstream dependency latency", "a connected service may be missing from the data", "shows whether the delay comes from this service or a dependency")
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
		profile := adaptive.GlobalStore.GetProfile(nodeID)
		if profile.EvaluateLoad(st.Load).IsAnomaly || profile.EvaluateQueue(st.QueueLength).IsAnomaly {
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
// CONFIDENCE NARRATIVE - plain-English explanation of confidence components
// ============================================================================

// ConfidenceNarrative converts raw confidence components into a human-readable
// explanation that an SRE can act on, rather than a numeric score alone.
func ConfidenceNarrative(score float64, determinism, graphCoverage, residualExplained, roleConsistency float64, latentRisk, fallbackReason string) string {
	if latentRisk == "HIGH" {
		if graphCoverage < 0.25 {
			return "I cannot safely name one cause yet because most observed services are not represented in the current service map. Add service data for the missing services, then run the check again."
		}
		if determinism < 0.3 {
			return "I am not fully sure because the likely cause changed between recent checks. The incident may still be moving, so review the service list before taking action."
		}
		return "I am not fully sure because a connected service may be involved but is not sending enough data yet."
	}
	if score >= 0.75 {
		return "I am confident enough to act: the same likely cause is showing up consistently, enough services were checked, and the evidence points in one direction."
	}
	reasons := make([]string, 0, 4)
	if determinism < 0.5 {
		reasons = append(reasons, "the likely cause changed between checks")
	}
	if graphCoverage < 0.5 {
		reasons = append(reasons, fmt.Sprintf("only %d%% of services were connected clearly enough to explain", int(graphCoverage*100)))
	}
	if residualExplained < 0.4 {
		reasons = append(reasons, "some load changes are still unexplained")
	}
	if roleConsistency < 0.5 {
		reasons = append(reasons, "different checks disagree about which service started it")
	}
	if len(reasons) == 0 {
		return fmt.Sprintf("I am %.0f%% sure. Review the suggested steps before acting.", score*100)
	}
	return fmt.Sprintf("I am %.0f%% sure because %s.", score*100, strings.Join(reasons, "; "))
}

// IncidentTitle generates a concise, operator-facing incident title.
func IncidentTitle(sem *FailureSemantics) string {
	if sem == nil {
		return "Waiting for service data"
	}
	root := firstNode(sem.RootCause, sem.Target)
	if root == "" {
		root = "a service"
	}
	switch sem.State {
	case StateHealthy:
		return "All checked services look healthy"
	case StateWatch:
		return fmt.Sprintf("%s needs watching", root)
	case StateRecovering:
		return fmt.Sprintf("%s is recovering", root)
	}
	if sem.BlastRadius > 1 {
		return fmt.Sprintf("%d services need help; likely start: %s", sem.BlastRadius, root)
	}
	return fmt.Sprintf("%s needs help", root)
}

// OperationalNarrative generates a full SRE-style incident narrative from the pipeline result.
// The output reads like a senior engineer describing what is happening and why.
func OperationalNarrative(sem *FailureSemantics) []string {
	if sem == nil {
		return []string{"I am waiting for enough service data to explain what is happening."}
	}
	lines := make([]string, 0, 6)
	root := firstNode(sem.RootCause, sem.Target)
	if root == "" {
		root = "the selected service"
	}

	switch sem.State {
	case StateHealthy:
		return []string{"What is happening: all checked services look healthy right now.", "Why: incoming work is staying within available capacity.", "How to proceed: keep monitoring; no fix is needed."}
	case StateWatch:
		lines = append(lines, fmt.Sprintf("What is happening: %s is starting to show pressure, but it has not crossed a failure threshold.", root))
	case StateRecovering:
		return []string{fmt.Sprintf("What is happening: %s is recovering.", root), "Why: recent pressure is going down instead of spreading.", "How to proceed: keep monitoring until the service stays stable."}
	default:
		lines = append(lines, fmt.Sprintf("What is happening: %s is the service most likely starting the current problem.", root))
	}

	if sem.BlastRadius > 1 {
		lines = append(lines, fmt.Sprintf("Why it matters: %d services are under pressure in the same time window, so the issue may spread if load is not reduced.", sem.BlastRadius))
	} else if sem.RootCause != "" {
		lines = append(lines, "Why it matters: the service is receiving more work or holding more backlog than it can comfortably process.")
	}

	if len(sem.Timeline) > 0 {
		lines = append(lines, "How it appears to be happening: "+strings.Join(sem.Timeline, " "))
	}
	if len(sem.Remediation) > 0 {
		lines = append(lines, "How to fix first: "+sem.Remediation[0].Action+". Reason: "+sem.Remediation[0].Rationale+".")
	}
	if len(sem.SafetyBlockers) > 0 {
		lines = append(lines, "Before acting: more proof is needed because "+strings.Join(sem.SafetyBlockers, ", ")+".")
	}
	return lines
}

// ExploreQuestion answers a natural-language operator question using deterministic
// pattern matching over the failure semantics and pipeline result.
// No LLM. No external calls. Pure signal-to-language translation.
func ExploreQuestion(question string, sem *FailureSemantics, confidenceNarrative, incidentTitle string) ExploreAnswer {
	q := strings.ToLower(strings.TrimSpace(question))
	root := firstNode(sem.RootCause, sem.Target)
	if root == "" {
		root = "the selected service"
	}

	if containsAny(q, "why", "cause", "root", "origin", "start") {
		return ExploreAnswer{
			Question: question,
			Answer:   strings.Join(OperationalNarrative(sem), "\n"),
			Category: "why",
			Evidence: sem.Evidence,
			Actions:  sem.Remediation,
		}
	}

	if containsAny(q, "do", "fix", "remediat", "action", "resolve", "mitigat", "help", "how") {
		if len(sem.Remediation) == 0 {
			return ExploreAnswer{Question: question, Answer: "I do not have a safe fix yet. Keep collecting service data until the likely cause is clearer.", Category: "how"}
		}
		steps := make([]string, 0, len(sem.Remediation))
		for _, r := range sem.Remediation {
			steps = append(steps, fmt.Sprintf("%d. %s. Why: %s. Expected result: %s.", r.Priority, r.Action, r.Rationale, r.ExpectedEffect))
		}
		return ExploreAnswer{Question: question, Answer: "Try these steps in order:\n" + strings.Join(steps, "\n"), Category: "how", Actions: sem.Remediation}
	}

	if containsAny(q, "confidence", "trust", "certain", "sure") {
		return ExploreAnswer{Question: question, Answer: confidenceNarrative, Category: "confidence", Evidence: sem.Evidence}
	}

	if containsAny(q, "evidence", "metric", "data", "signal", "telemetry", "number", "check") {
		if len(sem.Evidence) == 0 {
			return ExploreAnswer{Question: question, Answer: "No evidence is available yet. The system needs at least 4 samples per service before it can explain the issue.", Category: "evidence"}
		}
		parts := make([]string, 0, len(sem.Evidence))
		for _, ev := range sem.Evidence {
			node := ev.NodeID
			if node == "" {
				node = "system"
			}
			parts = append(parts, fmt.Sprintf("%s: %s", node, ev.Interpretation))
		}
		return ExploreAnswer{Question: question, Answer: "What I checked:\n" + strings.Join(parts, "\n"), Category: "evidence", Evidence: sem.Evidence}
	}

	if containsAny(q, "service", "which", "who", "node", "container") {
		return ExploreAnswer{Question: question, Answer: fmt.Sprintf("The service to look at first is %s. Current status: %s. Services affected: %d.", root, sem.State, sem.BlastRadius), Category: "service", Evidence: sem.Evidence}
	}

	return ExploreAnswer{
		Question: question,
		Answer:   fmt.Sprintf("%s\n\n%s", incidentTitle, strings.Join(OperationalNarrative(sem), "\n")),
		Category: "summary",
		Evidence: sem.Evidence,
		Actions:  sem.Remediation,
	}
}

// ExploreAnswer is the structured response from ExploreQuestion.
type ExploreAnswer struct {
	Question string              `json:"question"`
	Answer   string              `json:"answer"`
	Category string              `json:"category"`
	Evidence []FailureEvidence   `json:"evidence,omitempty"`
	Actions  []RemediationAction `json:"actions,omitempty"`
}

func firstNode(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
