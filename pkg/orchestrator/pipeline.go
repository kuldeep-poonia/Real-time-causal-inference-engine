package orchestrator

import (
	"fmt"
	"log"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"absia/pkg/bridge"
	"absia/pkg/data"
	"absia/pkg/metricsstore"
	"absia/pkg/policy"
	"absia/pkg/telemetry"
	"absia/pkg/topology"

	phase1 "absia/internal/intelligence/phase1_signal"
	phase2 "absia/internal/intelligence/phase2_pattern"
	phase3 "absia/internal/intelligence/phase3_causal"
	phase4 "absia/internal/intelligence/phase4_explanation"
	phase5 "absia/internal/intelligence/phase5_insight"
	"absia/pkg/cache"
)

//
// RESULT TYPES
//

// SafetyResult holds the three mandatory safety-gate outputs.
// No API response is returned without these being evaluated.
type SafetyResult struct {
	LatentRisk phase5.LatentRiskReport
	Confidence phase5.ConfidenceReport
	Fallback   phase5.FallbackDecision
	Fusion     phase5.FusionResult
}

// phase1NodeState mirrors phase1_signal.NodeState without an import cycle.
type phase1NodeState struct {
	Load            float64
	ArrivalRate     float64
	ServiceRate     float64
	QueueLength     float64
	ProcessingDelay float64
	Timestamp       float64
}

// PipelineResult carries the output of all 5 phases plus the safety gate.
type PipelineResult struct {
	// Phase 1
	Phase1NodeState *phase1NodeState

	// Phase 2
	Phase2Patterns      []*phase2.Pattern
	Phase2Dynamics      *phase2.DynamicsIndicator
	Phase2FeatureVector *phase2.FeatureVector

	// Phase 3
	Phase3Result *phase3.InferenceResult
	Phase3Graph  *phase3.Graph

	// Phase 3 extensions
	InterventionResults []phase3.InterventionResult
	BackdoorEffects     map[string]float64
	PhysicsRootCauses   []phase3.RootCauseResult

	// Phase 4
	Phase4CausalGraph *phase4.CausalGraph
	Phase4Dataset     *phase4.Dataset
	Phase4Explanation *phase4.Explanation

	// Phase 5
	Phase5BeliefState phase5.BeliefState
	Phase5Actions     []phase5.Action
	Phase5Policy      *phase5.Policy

	// Safety gate is mandatory and always evaluated.
	SafetyResult *SafetyResult

	ExecutionTimeMS   float64 // populated for every run
	ErrorsEncountered []string
	DataSource        string // "real" | "synthetic"
}

// PrimaryRootCause returns the best root-cause node the pipeline has identified.
// Queue physics wins when present because it is derived directly from lambda/mu
// pressure and backlog, not just inferred graph topology.
func (pr *PipelineResult) PrimaryRootCause() string {
	if pr == nil {
		return ""
	}
	if len(pr.PhysicsRootCauses) > 0 && pr.PhysicsRootCauses[0].NodeID != "" {
		return pr.PhysicsRootCauses[0].NodeID
	}
	if pr.Phase4Explanation != nil && len(pr.Phase4Explanation.Causes) > 0 {
		return pr.Phase4Explanation.Causes[0]
	}
	if pr.Phase3Result != nil && len(pr.Phase3Result.Causes) > 0 {
		return pr.Phase3Result.Causes[0].Node
	}
	if pr.Phase3Result != nil {
		return pr.Phase3Result.Target
	}
	return ""
}

// GetNodeStatesMap extracts the NodeState for each node from the phase3 graph.
// This is used by the failure semantics engine to determine node health.
func (pr *PipelineResult) GetNodeStatesMap() map[string]phase3.NodeState {
	if pr == nil || pr.Phase3Graph == nil {
		return nil
	}
	return buildNodeStatesMap(pr.Phase3Graph)
}

//
// PACKAGE-LEVEL CONFIGURATION
//

// pipelineSeed is the seed used for all deterministic random operations.
// Defaults to 42; overrideable via SetSeed before first pipeline call.
var pipelineSeed int64 = 42

// pipelinePolicyStore is the optional policy persistence backend.
// Nil means no persistence (cold-start on every run).
var pipelinePolicyStore *policy.Store

// pipelineMu guards pipelineSeed and pipelinePolicyStore.
var pipelineMu sync.RWMutex

// SetSeed configures the global pipeline seed.
// Must be called before the first ExecuteFullPipeline call.
func SetSeed(seed int64) {
	pipelineMu.Lock()
	defer pipelineMu.Unlock()
	if seed <= 0 {
		seed = 42
	}
	pipelineSeed = seed
}

// SetPolicyStore injects a policy persistence store.
// When set, trained policies are loaded (warmstart) and saved after each run.
func SetPolicyStore(s *policy.Store) {
	pipelineMu.Lock()
	defer pipelineMu.Unlock()
	pipelinePolicyStore = s
}

func getSeed() int64 {
	pipelineMu.RLock()
	defer pipelineMu.RUnlock()
	return pipelineSeed
}

func getPolicyStore() *policy.Store {
	pipelineMu.RLock()
	defer pipelineMu.RUnlock()
	return pipelinePolicyStore
}

//
// PER-TARGET RANKING STATE
// Fix: was a single global slice, causing concurrent requests for different
// targets to corrupt each other's ranking instability signal.
// Now keyed by target node ID so each target has an independent prior.
//

var (
	prevRankingCache cache.CacheStore
	temporalCache    cache.CacheStore
)

func init() {
	// The capacities will eventually be configurable via config layer
	prevRankingCache = cache.NewMemoryLRU(1000)
	temporalCache = cache.NewMemoryLRU(1000)
}

// GetPrevRanking returns a copy of the last stored cause ranking for target.
// Returns nil on the first call for a given target, which preserves first-call
// behaviour for lgRankInstability which returns 0.0 when prev is nil.
func GetPrevRanking(target string) []string {
	if val, ok := prevRankingCache.Get(target); ok {
		src := val.([]string)
		if len(src) == 0 {
			return nil
		}
		cp := make([]string, len(src))
		copy(cp, src)
		return cp
	}
	return nil
}

// setPrevRanking stores the current cause ranking for the given target.
func setPrevRanking(target string, causes []string) {
	dst := make([]string, len(causes))
	copy(dst, causes)
	prevRankingCache.Set(target, dst)
}

//
// PUBLIC ENTRY POINTS
//

// ExecuteFullPipelineFromStore is the sole production entry point.
// It requires a non-nil store with real container metrics.
// If the store is nil or has insufficient data it returns an explicit error
// rather than silently substituting synthetic data.
// Callers must wait for at least 2 containers to accumulate 4 samples each
// (~32 seconds after startup with the default 8s collection interval).
func ExecuteFullPipelineFromStore(
	targetNodeIDHint string,
	store *metricsstore.Store,
	topoMgr *topology.Manager,
) (*PipelineResult, error) {

	startTime := time.Now()

	result := &PipelineResult{ErrorsEncountered: make([]string, 0)}
	
	defer func() {
		telemetry.Get().Increment("absia_pipeline_executions")
		if result != nil && result.ExecutionTimeMS > 0 {
			telemetry.Get().RecordTime("absia_avg_execution_ms", result.ExecutionTimeMS)
		} else {
			telemetry.Get().RecordTime("absia_avg_execution_ms", float64(time.Since(startTime).Nanoseconds())/1e6)
		}
		
		if result != nil && result.SafetyResult != nil {
			if result.SafetyResult.Fallback.IsUnknown || result.SafetyResult.Confidence.State == phase5.UnknownState {
				telemetry.Get().Increment("absia_safety_rejections")
			}
		}
	}()

	// Real data requirement.
	// No synthetic fallback. If the store has no data yet, return a clear
	// error so the API can surface a meaningful "still collecting" message.
	if store == nil || !store.HasRealData() {
		return nil, fmt.Errorf("no real data available yet: metrics are collected every 8 seconds, real analysis starts after ~32 seconds")
	}
	if err := validateStoreData(store); err != nil {
		return nil, err
	}

	realisticData := convertStoreToDataset(store)
	if realisticData == nil || len(realisticData.Points) < 4 {
		return nil, fmt.Errorf("insufficient data: need at least 4 samples per container (collecting every 8s, real analysis starts after ~32s)")
	}

	avgMetricQuality := 0.6 // default to mid-quality if we can't determine
	if store != nil {
		totalQ := 0.0
		count := 0
		for _, nodeID := range realisticData.Nodes {
			if sample, ok := store.GetLatestSample(nodeID); ok {
				totalQ += sample.MetricQuality
				count++
			}
		}
		if count > 0 {
			avgMetricQuality = totalQ / float64(count)
		}
	}

	result.DataSource = "real"

	log.Printf("[ORCHESTRATOR] Starting pipeline: nodes=%v timesteps=%d",
		realisticData.Nodes, len(realisticData.Points))

	seed := getSeed()
	ps := getPolicyStore()

	//
	// PHASE 1: SIGNAL PHYSICS (WIRED DYNAMIC AGENT)
	//
	log.Println("[ORCHESTRATOR] Phase 1: signal physics...")

	schema := phase1.NewSignalSchema([]string{"ArrivalRate", "ServiceRate", "QueueLength"})
	p1Manager := phase1.NewManager(schema, 8.0, len(realisticData.Points), 0.2)
	aggregator := phase1.NewAggregator(schema, p1Manager, 8.0)

	for _, nodeID := range realisticData.Nodes {
		samples := store.GetSamples(nodeID)
		for _, s := range samples {
			aggregator.Add(nodeID, "ArrivalRate", s.ArrivalRate, s.Timestamp)
			aggregator.Add(nodeID, "ServiceRate", s.ServiceRate, s.Timestamp)
			aggregator.Add(nodeID, "QueueLength", s.QueueLength, s.Timestamp)
		}
	}
	
	// Flush aggregator into the manager's processors
	aggregator.FlushAll()
	
	// Wait a tiny bit for the async channels in Phase 1 to drain
	time.Sleep(50 * time.Millisecond)

	primaryNode := targetNodeFromHintOrDefault(targetNodeIDHint, realisticData.Nodes)
	p1Proc := p1Manager.GetProcessor(primaryNode)
	p1State := p1Proc.GetNodeState()

	p1Load := p1State.Load
	result.Phase1NodeState = &phase1NodeState{
		Load:            p1Load,
		ArrivalRate:     p1State.ArrivalRate,
		ServiceRate:     p1State.ServiceRate,
		QueueLength:     p1State.QueueLength,
		ProcessingDelay: p1State.ProcessingDelay,
		Timestamp:       p1State.Timestamp,
	}
	log.Printf("  -> rho=%.3f lambda=%.2f mu=%.2f", p1Load, p1State.ArrivalRate, p1State.ServiceRate)

	// PHASE 2: FULL PATTERN DETECTION
	//
	log.Println("[ORCHESTRATOR] Phase 2: full pattern detection...")

	// Use the newly wired Phase 1 smoothed matrix instead of raw data
	signalMatrix := make([][]float64, len(realisticData.Points))
	for t := 0; t < len(realisticData.Points); t++ {
		signalMatrix[t] = make([]float64, len(realisticData.Nodes))
	}
	
	for j, nodeID := range realisticData.Nodes {
		proc := p1Manager.GetProcessor(nodeID)
		mat := proc.GetMatrix()
		if len(mat.Values) == 0 {
			continue
		}
		arIdx := mat.FeatureIdx["ArrivalRate"]
		
		offset := len(realisticData.Points) - len(mat.Values)
		if offset < 0 {
			offset = 0
		}
		for t := 0; t < len(mat.Values); t++ {
			if t+offset < len(signalMatrix) {
				signalMatrix[t+offset][j] = mat.Values[t][arIdx]
			}
		}
	}

	dynamics := phase2.ComputeDynamicsIndicator(signalMatrix)
	result.Phase2Dynamics = &dynamics

	if len(signalMatrix) > 4 && len(signalMatrix[0]) > 0 {
		cols := len(signalMatrix[0])
		allRegimes := make([][]phase2.Regime, cols)
		for j := 0; j < cols; j++ {
			col := extractColumnFromMatrix(signalMatrix, j)
			allRegimes[j] = phase2.DetectRegimes(col)
		}
		fv := phase2.ExtractFeatures(signalMatrix)
		result.Phase2FeatureVector = &fv

		patternsVal := phase2.BuildPatterns(signalMatrix, allRegimes, fv)
		patterns := make([]*phase2.Pattern, len(patternsVal))
		for i := range patternsVal {
			p := patternsVal[i]
			patterns[i] = &p
		}
		result.Phase2Patterns = patterns
		log.Printf("  -> %d regimes, %d features, %d patterns", cols, len(fv.Signals), len(patterns))
	} else {
		patterns := make([]*phase2.Pattern, 0)
		if p1Load > 1.0 {
			patterns = append(patterns, &phase2.Pattern{
				Start: 0, End: len(realisticData.Points),
				Type: phase2.Drift, Confidence: math.Min(0.5+0.5*(p1Load-1.0), 0.99),
				SignalsInvolved: []int{0},
			})
		}
		if dynamics.Type == phase2.DivergingDynamics {
			patterns = append(patterns, &phase2.Pattern{
				Start: len(realisticData.Points) * 3 / 4, End: len(realisticData.Points),
				Type: phase2.Spike, Confidence: math.Min(dynamics.DivergenceRate, 0.99),
				SignalsInvolved: []int{0, 1},
			})
		}
		result.Phase2Patterns = patterns
	}
	log.Printf("  -> dynamics=%s patterns=%d", dynamics.Type, len(result.Phase2Patterns))

	// Wire BuildSystemState
	var patternsSlice []phase2.Pattern
	for _, p := range result.Phase2Patterns {
		patternsSlice = append(patternsSlice, *p)
	}

	// Retrieve temporal memory from cache
	var temporalMemory *phase2.SystemMemory
	if val, ok := temporalCache.Get(primaryNode); ok {
		temporalMemory = val.(*phase2.SystemMemory)
	} else {
		temporalMemory = &phase2.SystemMemory{
			EnergyHistory:  make([]float64, 0),
			StateHistory:   make([]string, 0),
			PatternHistory: make([]phase2.PatternType, 0),
		}
	}

	systemState := phase2.BuildSystemState(signalMatrix, patternsSlice, temporalMemory)
	
	// Save updated temporal memory back to cache
	temporalCache.Set(primaryNode, temporalMemory)
	log.Printf("  -> temporal system state: %s (confidence: %.2f)", systemState.Type, systemState.Confidence)

	//
	// PHASE 3: CAUSAL GRAPH DISCOVERY + FULL CAUSAL ENGINE
	//
	log.Println("[ORCHESTRATOR] Phase 3: causal graph + full causal engine...")

	temporalGraph := buildTemporalGraph(realisticData)

	// Create PCMCI engine and discover graph
	pcmciEngine := phase3.NewPCMCIEngine(topoMgr)
	
	seriesMap := make(map[string][]float64)
	for _, nodeID := range realisticData.Nodes {
		// Exclude infrastructure/observability services from causal candidacy.
		// These services (e.g., the ABSIA engine itself) should not participate
		// in root cause ranking unless explicitly configured.
		if isInfrastructureService(nodeID) {
			log.Printf("  -> excluding infrastructure service from causal graph: %s", nodeID)
			continue
		}
		seriesMap[nodeID] = data.ExtractTimeSeries(realisticData, nodeID)
	}
	
	discoveredGraph := pcmciEngine.DiscoverGraph(seriesMap, 4)
	
	log.Printf("  -> discovered: %d nodes %d edges",
		len(discoveredGraph.Nodes), len(discoveredGraph.Edges))

	phase3.AssignTimestampsFromTopologicalOrder(discoveredGraph)

	for _, nodeID := range realisticData.Nodes {
		series := data.ExtractTimeSeries(realisticData, nodeID)
		if node, ok := discoveredGraph.Nodes[nodeID]; ok {
			node.Series = series
			var ns phase3.NodeState
			if sample, ok := store.GetLatestSample(nodeID); ok {
				sr := sample.ServiceRate
				if sr <= 0 {
					sr = 1.0
				}
				ns = phase3.NodeState{
					ArrivalRate: sample.ArrivalRate, ServiceRate: sr,
					Load: sample.ArrivalRate / sr, QueueLength: sample.QueueLength,
					Timestamp: node.State.Timestamp,
					DominantSignal: sample.DominantSignal,
				}
			}
			if ns.ServiceRate <= 0 {
				ns = phase3.NodeState{
					ArrivalRate: 0.5, ServiceRate: 1.0,
					Load: 0.5, QueueLength: 0,
					Timestamp: node.State.Timestamp,
					DominantSignal: "none",
				}
			}
			node.State = ns
		}
	}
	for _, e := range discoveredGraph.Edges {
		if fn, ok := discoveredGraph.Nodes[e.From]; ok {
			e.SourceSeries = fn.Series
		}
		if tn, ok := discoveredGraph.Nodes[e.To]; ok {
			e.TargetSeries = tn.Series
		}
	}

	discoveredGraph = phase3.EnrichGraphWithCausalIdentification(
		discoveredGraph,
		temporalGraph,
		phase3.DefaultIdentificationConfig(),
	)
	log.Printf("  -> causal identification enrichment applied")

	interventionResults := phase3.RunIntervention(discoveredGraph, temporalGraph,
		phase3.InterventionConfig{
			DeltaFactor: 1.0, MinEffect: 0.01, Epsilon: 1e-6,
			Boost: 1.2, Decay: 0.8,
			TimeTolerance: time.Second * 60, LagSteps: []int{1, 2},
		})
	result.InterventionResults = interventionResults
	log.Printf("  -> RunIntervention: %d edges evaluated", len(interventionResults))

	graphMgr := phase3.NewGraphManager(discoveredGraph, seed)
	graphMgr.Update(interventionResults, phase3.GraphManagerConfig{
		LearningRate:      0.05,
		MinProb:           0.01,
		MaxGraphs:         5,
		ComplexityPenalty: 0.01,
		ProbFloor:         0.01,
		MaxHistory:        10,
	})
	if len(graphMgr.Graphs) > 0 && graphMgr.Graphs[0].Graph != nil {
		for _, managed := range graphMgr.Graphs[0].Graph.Edges {
			for _, e := range discoveredGraph.Edges {
				if e.From == managed.From && e.To == managed.To {
					e.ExistenceProb = managed.ExistenceProb
					break
				}
			}
		}
		log.Printf("  -> GraphManager updated: best graph prob=%.4f", graphMgr.Graphs[0].Probability)
	}

	targetNodeID := resolveTarget(targetNodeIDHint, realisticData.Nodes, discoveredGraph)
	nodeStatesMap := buildNodeStatesMap(discoveredGraph)

	if len(realisticData.Points) < 10 {
		log.Printf("[ORCHESTRATOR] Phase 3 skipped: need 10+ samples, have %d", len(realisticData.Points))
		result.ErrorsEncountered = append(result.ErrorsEncountered, 
			fmt.Sprintf("Phase 3 skipped: need 10+ samples, have %d; using queue-physics fallback", len(realisticData.Points)))
		if finishWithLocalPhysicsRoot(result, targetNodeID, nodeStatesMap, startTime) {
			return result, nil
		}
		result.SafetyResult = evaluateSafetyGateEmpty(targetNodeID)
		result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6
		return result, nil
	}

	const causalMinProbability = 0.1
	const causalMinStrength = 0.05

	hypotheses := phase3.BuildCausalHypotheses(discoveredGraph,
		phase3.CausalBuilderConfig{MinProbability: causalMinProbability, MinStrength: causalMinStrength, MaxCauses: 5})
	log.Printf("  -> hypotheses pre-filter: %d", len(hypotheses))

	if len(hypotheses) == 0 {
		result.ErrorsEncountered = append(result.ErrorsEncountered,
			"Phase 3: no lagged causal hypotheses; using queue-physics fallback when available")
		if finishWithLocalPhysicsRoot(result, targetNodeID, nodeStatesMap, startTime) {
			return result, nil
		}
		result.SafetyResult = evaluateSafetyGateEmpty(targetNodeID)
		result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6
		return result, nil
	}

	causalGraph := pruneWeakCausalEdges(discoveredGraph, causalMinProbability, causalMinStrength)
	if len(causalGraph.Edges) == 0 {
		result.ErrorsEncountered = append(result.ErrorsEncountered,
			"Phase 3: no usable causal edges after pruning; using queue-physics fallback when available")
		if finishWithLocalPhysicsRoot(result, targetNodeID, nodeStatesMap, startTime) {
			return result, nil
		}
		result.SafetyResult = evaluateSafetyGateEmpty(targetNodeID)
		result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6
		return result, nil
	}

	dsepEnrichment := RunDSeparationEnrichment(causalGraph)
	backdoorEnrichment := RunBackdoorEnrichment(causalGraph, temporalGraph, dsepEnrichment)
	log.Printf("  -> d-sep enrichment: %d confirmed %d confounded edges",
		len(dsepEnrichment.DSepConfirmed), len(dsepEnrichment.ConfoundedPairs))

	for key, effect := range backdoorEnrichment.Effects {
		for _, e := range causalGraph.Edges {
			if e.From+"->"+e.To == key {
				if effect != 0 {
					e.CausalStrength = effect
				}
				break
			}
		}
	}

	hypotheses = applyDSeparationFilter(hypotheses, causalGraph)
	hypotheses = filterHypothesesForTarget(hypotheses, targetNodeID)
	log.Printf("  -> D-sep filter: %d hypotheses survive for target=%s", len(hypotheses), targetNodeID)

	// If the target has no inbound causal edges (it is a traffic source, not a
	// downstream target), construct a reverse hypothesis from its outgoing
	// neighbors. This represents backpressure: downstream services affecting
	// the target's performance via feedback loops.
	if len(hypotheses) == 0 {
		reverseH := buildReverseHypothesis(causalGraph, targetNodeID)
		if reverseH != nil {
			hypotheses = append(hypotheses, *reverseH)
			log.Printf("  -> constructed reverse (backpressure) hypothesis for %s with %d edges",
				targetNodeID, len(reverseH.Subgraph.Edges))
		}
	}

	if len(hypotheses) == 0 {
		result.ErrorsEncountered = append(result.ErrorsEncountered,
			"Phase 3: no causal hypotheses for requested target; using queue-physics fallback when available")
		if finishWithLocalPhysicsRoot(result, targetNodeID, nodeStatesMap, startTime) {
			return result, nil
		}
		result.SafetyResult = evaluateSafetyGateEmpty(targetNodeID)
		result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6
		return result, nil
	}

	for i := range hypotheses {
		if hypotheses[i].Target == "" {
			hypotheses[i].Target = targetNodeID
		}
	}

	phase3Results := phase3.RunCausalInference(hypotheses,
		phase3.InferenceConfig{MinProbability: 0.0, MinConfidence: 0.0, TopK: 1})

	if len(phase3Results) > 0 && len(phase3Results[0].Causes) > 0 {
		result.Phase3Result = &phase3Results[0]
		result.Phase3Graph = causalGraph
		log.Printf("  -> root cause: target=%s score=%.4f conf=%.4f",
			phase3Results[0].Target, phase3Results[0].Score, phase3Results[0].Confidence)
	} else {
		result.ErrorsEncountered = append(result.ErrorsEncountered,
			"Phase 3: no causal drivers; using queue-physics fallback when available")
		if finishWithLocalPhysicsRoot(result, targetNodeID, nodeStatesMap, startTime) {
			return result, nil
		}
		result.SafetyResult = evaluateSafetyGateEmpty(targetNodeID)
		result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6
		return result, nil
	}

	// Safe: result.Phase3Result is guaranteed non-nil here (set in the if-branch above).
	if len(result.Phase3Result.Causes) > 0 {
		backdoorEffects := make(map[string]float64)
		for _, cause := range result.Phase3Result.Causes {
			br := phase3.ComputeBackdoorEffect(causalGraph, temporalGraph,
				cause.Node, targetNodeID)
			backdoorEffects[cause.Node] = br.Effect
		}
		result.BackdoorEffects = backdoorEffects
		log.Printf("  -> backdoor effects: %v", backdoorEffects)
	}

	physicsRoots := phase3.FindRootCauseByPropagation(causalGraph, nodeStatesMap, targetNodeID)
	if len(physicsRoots) == 0 {
		if localRoot, ok := findLocalPhysicsRoot(nodeStatesMap, targetNodeID); ok {
			physicsRoots = []phase3.RootCauseResult{localRoot}
		}
	}
	result.PhysicsRootCauses = physicsRoots
	if len(physicsRoots) > 0 {
		log.Printf("  -> physics root: %s score=%.4f", physicsRoots[0].NodeID, physicsRoots[0].Score)
	}

	//
	// PHASE 4: EXPLANATION
	//
	log.Println("[ORCHESTRATOR] Phase 4: explanation generation...")

	phase4Dataset := bridge.ConvertPhase3ResultToPhase4Dataset(result.Phase3Graph)
	phase4Graph := bridge.ConvertPhase3ResultToPhase4Graph(*result.Phase3Result, result.Phase3Graph, phase4Dataset)

	if err := bridge.ValidateConversionPhase3ToPhase4(phase4Graph); err != nil {
		result.ErrorsEncountered = append(result.ErrorsEncountered, "Phase 4 graph conversion: "+err.Error())
		result.SafetyResult = evaluateSafetyGateEmpty(targetNodeID)
		result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6
		return result, nil
	}

	result.Phase4CausalGraph = phase4Graph
	result.Phase4Dataset = phase4Dataset

	explanation := phase4.GenerateExplanation(phase4Graph, phase4Dataset, targetNodeID)
	result.Phase4Explanation = &explanation
	log.Printf("  -> %d causes: %v", len(explanation.Causes), explanation.Causes)

	// Update per-target ranking for next call's instability check.
	prevRanking := GetPrevRanking(targetNodeID)
	setPrevRanking(targetNodeID, explanation.Causes)

	if len(explanation.Causes) == 0 {
		result.SafetyResult = evaluateSafetyGateEmpty(targetNodeID)
		result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6
		return result, nil
	}

	//
	// PHASE 5: RL POLICY + SAFETY GATE
	// Fix 4: warmstart from persisted weights; persist after training.
	//
	log.Println("[ORCHESTRATOR] Phase 5: RL policy + safety gate...")

	staticValues := make(map[string]float64)
	for nodeID := range phase4Graph.Nodes {
		staticValues[nodeID] = phase4Graph.Nodes[nodeID].Value
	}

	beliefState := bridge.ConvertPhase4ExplanationToPhase5BeliefState(explanation, staticValues)
	result.Phase5BeliefState = beliefState

	if err := bridge.ValidateConversionPhase4ToPhase5(beliefState); err != nil {
		result.ErrorsEncountered = append(result.ErrorsEncountered, "Phase 5 belief state: "+err.Error())
		result.SafetyResult = evaluateSafetyGateEmpty(targetNodeID)
		result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6
		return result, nil
	}

	nodeIDs := sortedKeys(phase4Graph.Nodes)
	actions := bridge.GenerateInterventionActions(explanation, nodeIDs)
	result.Phase5Actions = actions

	phase5Graph := bridge.ConvertPhase4GraphToPhase5Graph(phase4Graph)
	phase5Exp := bridge.ConvertPhase4ExplanationToPhase5Explanation(explanation)

	// Load prior policy weights for warmstart.
	var priorPolicy *phase5.Policy
	if ps != nil {
		if w, err := ps.Load(targetNodeID); err != nil {
			slog.Warn("policy store load failed; using random init",
				slog.String("target", targetNodeID),
				slog.Any("error", err))
		} else if w != nil {
			priorPolicy = &phase5.Policy{W: w.W, B: w.B}
			log.Printf("  -> RL policy warmstart from persisted weights: target=%s actions=%d", targetNodeID, len(w.W))
		}
	}

	var policy5 *phase5.Policy
	if priorPolicy != nil {
		policy5 = phase5.WarmstartTrain(priorPolicy, phase5Graph, beliefState, phase5Exp, actions, targetNodeID, 100, seed)
	} else {
		policy5 = phase5.TrainWithSeed(phase5Graph, beliefState, phase5Exp, actions, targetNodeID, 100, seed)
	}
	result.Phase5Policy = policy5
	log.Printf("  -> RL policy trained: %d actions (warmstart=%v)", len(actions), priorPolicy != nil)

	// Persist updated weights for next call.
	if ps != nil && policy5 != nil {
		w := &policy.Weights{W: policy5.W, B: policy5.B}
		if err := ps.Save(targetNodeID, w); err != nil {
			slog.Warn("policy store save failed",
				slog.String("target", targetNodeID),
				slog.Any("error", err))
		}
	}

	result.SafetyResult = evaluateSafetyGateFull(phase5Graph, phase5Exp, result.Phase3Result, targetNodeID, prevRanking, avgMetricQuality)

	if result.SafetyResult.Fallback.IsUnknown {
		log.Printf("  -> SAFETY GATE: UNKNOWN reasons=%v conf=%.3f risk=%s",
			result.SafetyResult.Fallback.Reasons,
			result.SafetyResult.Confidence.Score,
			result.SafetyResult.LatentRisk.Level.String())
	} else {
		log.Printf("  -> safety gate: %s conf=%.3f risk=%s",
			result.SafetyResult.Confidence.State.String(),
			result.SafetyResult.Confidence.Score,
			result.SafetyResult.LatentRisk.Level.String())
	}

	result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6 // Fix 5
	log.Println("[ORCHESTRATOR] Pipeline complete")
	return result, nil
}

//
// SAFETY GATE
//

func evaluateSafetyGateFull(
	graph *phase5.CausalGraph,
	exp phase5.Explanation,
	phase3Result *phase3.InferenceResult,
	target string,
	prevRanking []string,
	metricQuality float64,
) *SafetyResult {
	p3Root := ""
	if phase3Result != nil && len(phase3Result.Causes) > 0 {
		p3Root = phase3Result.Causes[0].Node
	}
	report := RunSafetyGate(graph, exp, prevRanking, target, metricQuality)
	fusion := phase5.FuseCausalResults(graph, p3Root, exp.Causes, target)
	return &SafetyResult{
		LatentRisk: report.LatentReport,
		Confidence: report.ConfidenceReport,
		Fallback:   report.FallbackDecision,
		Fusion:     fusion,
	}
}

func evaluateSafetyGateEmpty(target string) *SafetyResult {
	empty := phase5.Explanation{
		Causes: []string{}, Effects: map[string]float64{}, Uncertainty: map[string]float64{},
	}
	emptyFusion := phase5.FusionResult{}
	latent := phase5.AssessLatentRisk(nil, empty, nil, target)
	conf := phase5.ComputeConfidence(emptyFusion, nil, empty, latent, 0.5)
	fallback := phase5.EvaluateFallback(conf, latent, emptyFusion, nil, target)
	return &SafetyResult{LatentRisk: latent, Confidence: conf, Fallback: fallback, Fusion: emptyFusion}
}

func evaluateSafetyGateLocal(root phase3.RootCauseResult) *SafetyResult {
	score := localRootConfidence(root)

	// Derive components from actual telemetry instead of hardcoding to 1.0.
	// PosteriorPrecision: how precisely we can estimate load (bounded by score itself).
	posteriorPrecision := score
	// Determinism: stable if load is well below 1.0, unstable as it approaches/exceeds 1.0.
	determinism := math.Max(0.0, math.Min(1.0, 1.0-root.Load))
	// ResidualExplained: fraction of queue that is explainable by the load.
	residualExplained := score
	// RoleConsistency: 1.0 only when the root cause is unambiguous.
	roleConsistency := 0.5 // Physics fallback has no fusion corroboration.

	state := phase5.UnknownState
	switch {
	case score >= 0.75:
		state = phase5.ConfirmedState
	case score >= 0.45:
		state = phase5.ProbableState
	}

	// Latent risk is MEDIUM for physics fallback — we have no causal graph evidence.
	latent := phase5.LatentRiskReport{
		Level:             phase5.LatentRiskMedium,
		ResidualRatio:     residualExplained,
		PosteriorVariance: 1.0 - posteriorPrecision,
		SuspiciousNodes:   []string{},
	}
	conf := phase5.ConfidenceReport{
		Score: score,
		State: state,
		Components: phase5.ConfidenceComponents{
			PosteriorPrecision: posteriorPrecision,
			Determinism:        determinism,
			ResidualExplained:  residualExplained,
			RoleConsistency:    roleConsistency,
			LatentPenalty:      0.10, // Medium penalty for missing causal evidence
		},
	}
	fusion := phase5.FusionResult{RootCauses: []string{root.NodeID}}
	fallback := phase5.FallbackDecision{
		IsUnknown:         score < 0.45,
		RemediationPolicy: phase5.PolicyHumanReview,
		ConfidenceScore:   score,
		LatentLevel:       phase5.LatentRiskMedium,
	}
	return &SafetyResult{LatentRisk: latent, Confidence: conf, Fallback: fallback, Fusion: fusion}
}

func finishWithLocalPhysicsRoot(
	result *PipelineResult,
	target string,
	nodeStates map[string]phase3.NodeState,
	startTime time.Time,
) bool {
	root, ok := findLocalPhysicsRoot(nodeStates, target)
	if !ok {
		return false
	}

	confidence := localRootConfidence(root)
	result.PhysicsRootCauses = []phase3.RootCauseResult{root}
	result.Phase3Result = &phase3.InferenceResult{
		Target: target,
		Causes: []phase3.Cause{{
			Node:  root.NodeID,
			Score: root.Score,
		}},
		Score:      root.Score,
		Confidence: confidence,
	}
	result.SafetyResult = evaluateSafetyGateLocal(root)
	result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6
	log.Printf("  -> queue physics root: %s load=%.3f queue=%.2f conf=%.3f",
		root.NodeID, root.Load, root.QueueLength, confidence)
	return true
}

func findLocalPhysicsRoot(
	nodeStates map[string]phase3.NodeState,
	target string,
) (phase3.RootCauseResult, bool) {
	if len(nodeStates) == 0 {
		return phase3.RootCauseResult{}, false
	}

	if target != "" {
		if state, ok := nodeStates[target]; ok {
			if root, ok := buildLocalPhysicsRoot(target, target, state); ok {
				return root, true
			}
		}
	}

	best := phase3.RootCauseResult{}
	bestScore := -1.0
	for _, nodeID := range sortedKeys(nodeStates) {
		root, ok := buildLocalPhysicsRoot(nodeID, target, nodeStates[nodeID])
		if !ok {
			continue
		}
		if root.Score > bestScore {
			best = root
			bestScore = root.Score
		}
	}
	if bestScore < 0 {
		return phase3.RootCauseResult{}, false
	}
	return best, true
}

func buildLocalPhysicsRoot(
	nodeID string,
	target string,
	state phase3.NodeState,
) (phase3.RootCauseResult, bool) {
	state = normalizedNodeState(state)
	queueLength := math.Max(0, state.QueueLength)
	load := state.Load

	if load < 0.85 && queueLength < 100 && state.DominantSignal != "memory" && state.DominantSignal != "network" {
		return phase3.RootCauseResult{}, false
	}

	overloadScore := math.Max(0, load-1.0) * 3.0
	pressureScore := math.Max(0, load-0.85)
	queueScore := math.Min(math.Log1p(queueLength)/10.0, 1.5)
	score := overloadScore + pressureScore + queueScore

	if state.DominantSignal == "network" {
		// Network pressure is bounded 0-1.0, treat it as a direct score addition
		score += queueLength * 0.4
		score *= 1.3
	} else if state.DominantSignal == "memory" {
		// Memory pressure is bounded 0-2.0, treat it as a direct score addition
		score += queueLength * 0.4
		score *= 1.5
	}

	chain := &phase3.PropagationChain{
		RootNode:          nodeID,
		TargetNode:        target,
		TotalDelay:        state.ProcessingDelay,
		FinalStrength:     score,
		IsPhysicallyValid: nodeID == target || target == "",
	}

	explanation := fmt.Sprintf(
		"ROOT CAUSE: %s - arrival_rate=%.3f service_rate=%.3f rho=%.2f queue_length=%.2f",
		nodeID, state.ArrivalRate, state.ServiceRate, load, queueLength,
	)
	if load <= 1.0 {
		explanation = fmt.Sprintf(
			"ROOT CAUSE: %s - queue backlog remains high (queue_length=%.2f) with current rho=%.2f",
			nodeID, queueLength, load,
		)
	}

	return phase3.RootCauseResult{
		NodeID:              nodeID,
		Score:               score,
		IsOverloaded:        load > 1.0,
		QueueLength:         queueLength,
		Load:                load,
		PropagationChain:    chain,
		PhysicalExplanation: explanation,
	}, true
}

func normalizedNodeState(state phase3.NodeState) phase3.NodeState {
	if state.ServiceRate <= 0 {
		state.ServiceRate = 1.0
	}
	if state.Load <= 0 && state.ArrivalRate > 0 {
		state.Load = state.ArrivalRate / state.ServiceRate
	}
	if state.ProcessingDelay <= 0 && state.QueueLength > 0 {
		state.ProcessingDelay = state.QueueLength / state.ServiceRate
	}
	return state
}

func localRootConfidence(root phase3.RootCauseResult) float64 {
	score := 0.50
	if root.Load >= 1.0 {
		score += 0.10
	}
	if root.Load >= 1.05 {
		score += 0.10
	}
	score += math.Min(math.Log1p(math.Max(root.QueueLength, 0))/40.0, 0.14)
	return math.Max(0.45, math.Min(score, 0.74))
}

func pruneWeakCausalEdges(graph *phase3.Graph, minProbability, minStrength float64) *phase3.Graph {
	if graph == nil {
		return nil
	}
	pruned := &phase3.Graph{
		Nodes:   make(map[string]*phase3.Node, len(graph.Nodes)),
		Edges:   make([]*phase3.Edge, 0, len(graph.Edges)),
		Factors: graph.Factors,
	}
	for id, node := range graph.Nodes {
		pruned.Nodes[id] = node
	}
	for _, edge := range graph.Edges {
		if edge.ExistenceProb < minProbability {
			continue
		}
		if math.Abs(edge.CausalStrength) < minStrength {
			continue
		}
		if len(edge.SourceSeries) < 4 || len(edge.TargetSeries) < 4 {
			continue
		}
		edgeCopy := *edge
		pruned.Edges = append(pruned.Edges, &edgeCopy)
	}
	return pruned
}

//
// D-SEPARATION FILTER
//

func applyDSeparationFilter(
	hypotheses []phase3.CausalHypothesis,
	graph *phase3.Graph,
) []phase3.CausalHypothesis {
	if len(hypotheses) == 0 {
		return hypotheses
	}
	var filtered []phase3.CausalHypothesis
	for _, h := range hypotheses {
		hasActivePath := false
		for _, e := range h.Subgraph.Edges {
			if e.To == h.Target {
				if !phase3.IsDSeparated(h.Subgraph, e.From, h.Target, map[string]bool{}) {
					hasActivePath = true
					break
				}
			}
		}
		if !hasActivePath && len(h.Subgraph.Edges) == 0 {
			hasActivePath = true
		}
		if hasActivePath {
			filtered = append(filtered, h)
		}
	}
	if len(filtered) == 0 {
		return hypotheses
	}
	return filtered
}

func filterHypothesesForTarget(
	hypotheses []phase3.CausalHypothesis,
	target string,
) []phase3.CausalHypothesis {
	if target == "" || len(hypotheses) == 0 {
		return hypotheses
	}
	filtered := make([]phase3.CausalHypothesis, 0, len(hypotheses))
	for _, h := range hypotheses {
		if h.Target == target {
			filtered = append(filtered, h)
		}
	}
	return filtered
}

//
// DATA HELPERS
//

func convertStoreToDataset(store *metricsstore.Store) *data.Dataset {
	nodeIDs := store.GetAllNodeIDs()
	if len(nodeIDs) == 0 {
		return nil
	}

	// Qualify nodes with enough real samples for analysis.
	qualified := make([]string, 0, len(nodeIDs))
	maxLen := 0
	for _, id := range nodeIDs {
		s := store.GetArrivalRateSeries(id)
		if len(s) < 4 {
			continue
		}
		qualified = append(qualified, id)
		if len(s) > maxLen {
			maxLen = len(s)
		}
	}

	// Need at least one service with enough real samples.
	if len(qualified) == 0 || maxLen < 4 {
		return nil
	}

	ds := &data.Dataset{Points: make([]data.DataPoint, maxLen), Nodes: qualified}
	for t := 0; t < maxLen; t++ {
		ds.Points[t] = data.DataPoint{
			Timestamp: float64(t),
			Values:    make(map[string]float64),
			Missing:   make(map[string]bool),
		}
		for _, id := range qualified {
			series := store.GetArrivalRateSeries(id)
			if t < len(series) {
				ds.Points[t].Values[id] = series[t]
			} else {
				ds.Points[t].Values[id] = series[len(series)-1]
			}
		}
	}
	return ds
}

func buildNodeStatesMap(graph *phase3.Graph) map[string]phase3.NodeState {
	states := make(map[string]phase3.NodeState, len(graph.Nodes))
	for id, node := range graph.Nodes {
		states[id] = node.State
	}
	return states
}

func validateStoreData(store *metricsstore.Store) error {
	for _, id := range store.GetAllNodeIDs() {
		for i, sample := range store.GetSamples(id) {
			if !validFinite(sample.ArrivalRate) || sample.ArrivalRate < 0 {
				return fmt.Errorf("invalid sample for %s at index %d: arrival_rate must be >= 0", id, i)
			}
			if !validFinite(sample.ServiceRate) || sample.ServiceRate <= 0 {
				return fmt.Errorf("invalid sample for %s at index %d: service_rate must be > 0", id, i)
			}
			if !validFinite(sample.QueueLength) || sample.QueueLength < 0 {
				return fmt.Errorf("invalid sample for %s at index %d: queue_length must be >= 0", id, i)
			}
		}
	}
	return nil
}

func validFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func targetNodeFromHintOrDefault(hint string, nodes []string) string {
	if hint != "" {
		for _, node := range nodes {
			if node == hint {
				return node
			}
		}
	}
	if len(nodes) > 0 {
		return nodes[len(nodes)-1]
	}
	return "target"
}

// resolveTarget returns the node the caller requested if it exists in the
// dataset. Falls back to mostDownstreamNode when the hint is absent.
func resolveTarget(hint string, nodes []string, graph *phase3.Graph) string {
	if hint != "" {
		for _, n := range nodes {
			if n == hint {
				return hint
			}
		}
		// Hint provided but node not in dataset; it has no /ingest data yet.
		// Pick closest name match (prefix), then fall back.
		for _, n := range nodes {
			if len(hint) >= 3 && len(n) >= 3 {
				if n[:3] == hint[:3] {
					return n
				}
			}
		}
	}
	return mostDownstreamNode(graph, nodes)
}

//
// SIGNAL / MATRIX HELPERS
//

func buildSignalMatrix(dataset *data.Dataset) [][]float64 {
	if len(dataset.Points) == 0 || len(dataset.Nodes) == 0 {
		return nil
	}
	matrix := make([][]float64, len(dataset.Points))
	lastKnown := make([]float64, len(dataset.Nodes))
	for t, point := range dataset.Points {
		row := make([]float64, len(dataset.Nodes))
		for j, node := range dataset.Nodes {
			if !point.Missing[node] {
				row[j] = point.Values[node]
				lastKnown[j] = row[j]
			} else {
				row[j] = lastKnown[j]
			}
		}
		matrix[t] = row
	}
	return matrix
}

func extractColumnFromMatrix(matrix [][]float64, col int) []float64 {
	out := make([]float64, len(matrix))
	for i, row := range matrix {
		if col < len(row) {
			out[i] = row[col]
		}
	}
	return out
}

func buildTemporalGraph(dataset *data.Dataset) *phase3.TemporalGraph {
	events := make([]phase3.Event, 0)
	
	const stepSeconds = 15
	epoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	stepIdx := 0
	for _, point := range dataset.Points {
		t := epoch.Add(time.Duration(stepIdx) * stepSeconds * time.Second)
		for _, nodeID := range dataset.Nodes {
			if !point.Missing[nodeID] {
				events = append(events, phase3.Event{
					NodeID:     nodeID,
					Timestamp:  t,
					Value:      point.Values[nodeID],
					NoiseLevel: 0.1,
					Intensity:  1.0,
					Confidence: 1.0,
					Type:       "data_point",
				})
			}
		}
		stepIdx++
	}

	cfg := phase3.TimeConfig{
		BucketSize:    time.Duration(stepSeconds) * time.Second,
		MaxAllowedGap: time.Duration(stepSeconds*3) * time.Second,
	}

	return phase3.BuildTemporalGraph(events, cfg)
}

func mostDownstreamNode(graph *phase3.Graph, nodes []string) string {
	outCount := make(map[string]int)
	for _, n := range nodes {
		outCount[n] = 0
	}
	for _, e := range graph.Edges {
		outCount[e.From]++
	}
	sorted := make([]string, len(nodes))
	copy(sorted, nodes)
	sort.Strings(sorted)
	best := sorted[0]
	for _, n := range sorted {
		if outCount[n] < outCount[best] {
			best = n
		}
	}
	return best
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

//
// SUMMARY
//

func (pr *PipelineResult) Summary() string {
	s := "[PIPELINE SUMMARY]\n"
	if pr.Phase1NodeState != nil {
		s += fmt.Sprintf("Phase 1: rho=%.3f lambda=%.2f mu=%.2f source=%s\n",
			pr.Phase1NodeState.Load, pr.Phase1NodeState.ArrivalRate,
			pr.Phase1NodeState.ServiceRate, pr.DataSource)
	}
	if pr.Phase2Dynamics != nil {
		s += fmt.Sprintf("Phase 2: %d patterns dynamics=%s divergence=%.3f features=%v\n",
			len(pr.Phase2Patterns), pr.Phase2Dynamics.Type,
			pr.Phase2Dynamics.DivergenceRate, pr.Phase2FeatureVector != nil)
	}
	if pr.Phase3Result != nil {
		s += fmt.Sprintf("Phase 3: target=%s score=%.4f conf=%.4f\n",
			pr.Phase3Result.Target, pr.Phase3Result.Score, pr.Phase3Result.Confidence)
		if len(pr.BackdoorEffects) > 0 {
			s += fmt.Sprintf("  backdoor effects: %v\n", pr.BackdoorEffects)
		}
		if len(pr.PhysicsRootCauses) > 0 {
			s += fmt.Sprintf("  physics root: %s score=%.4f\n",
				pr.PhysicsRootCauses[0].NodeID, pr.PhysicsRootCauses[0].Score)
		}
	} else {
		s += "Phase 3: no root cause detected (stable system)\n"
	}
	if pr.Phase4Explanation != nil {
		s += fmt.Sprintf("Phase 4: %d causes: %v\n",
			len(pr.Phase4Explanation.Causes), pr.Phase4Explanation.Causes)
	}
	if pr.Phase5Policy != nil {
		s += fmt.Sprintf("Phase 5: policy trained actions=%d\n", len(pr.Phase5Actions))
	}
	if pr.SafetyResult != nil {
		sr := pr.SafetyResult
		s += fmt.Sprintf("Safety: state=%s score=%.3f risk=%s fallback=%v\n",
			sr.Confidence.State.String(), sr.Confidence.Score,
			sr.LatentRisk.Level.String(), sr.Fallback.IsUnknown)
		if sr.Fallback.IsUnknown && len(sr.Fallback.Reasons) > 0 {
			s += fmt.Sprintf("  fallback reasons: %v\n", sr.Fallback.Reasons)
		}
	}
	s += fmt.Sprintf("ExecutionTimeMS: %.1f\n", pr.ExecutionTimeMS)
	if len(pr.ErrorsEncountered) > 0 {
		s += fmt.Sprintf("Errors: %v\n", pr.ErrorsEncountered)
	}
	return s
}

//
// INFRASTRUCTURE EXCLUSION
//

// isInfrastructureService returns true if the given node ID belongs to an
// infrastructure or observability service that should not participate in
// root cause analysis. The exclusion list is governed by the ABSIA_EXCLUDE_SERVICES
// environment variable (comma-separated). By default, the absia container itself
// is always excluded.
func isInfrastructureService(nodeID string) bool {
	lower := strings.ToLower(nodeID)

	// Always exclude the absia analysis engine itself
	if strings.Contains(lower, "absia") {
		return true
	}

	// Check configurable exclusion list from environment
	excludeEnv := os.Getenv("ABSIA_EXCLUDE_SERVICES")
	if excludeEnv != "" {
		for _, svc := range strings.Split(excludeEnv, ",") {
			svc = strings.TrimSpace(strings.ToLower(svc))
			if svc != "" && strings.Contains(lower, svc) {
				return true
			}
		}
	}

	return false
}

//
// REVERSE HYPOTHESIS BUILDER
//

// buildReverseHypothesis constructs a causal hypothesis for a target node that
// has no inbound causal edges. This happens when the target is a traffic source
// (e.g., frontend) — it sends traffic to downstream services, but no service
// sends traffic TO it. In this case, the target's performance is affected by
// backpressure from its downstream neighbors.
//
// The hypothesis treats the target's outgoing neighbors as causes affecting it
// via backpressure. This is statistically valid: if downstream services are slow,
// the target accumulates pending requests, raising its queue length and latency.
func buildReverseHypothesis(graph *phase3.Graph, target string) *phase3.CausalHypothesis {
	if graph == nil {
		return nil
	}

	// Collect all outgoing edges from the target
	var reverseEdges []*phase3.Edge
	for _, e := range graph.Edges {
		if e.From == target {
			// Create a reversed edge: downstream -> target (backpressure direction)
			rev := &phase3.Edge{
				From:           e.To,
				To:             target,
				ExistenceProb:  e.ExistenceProb * 0.8, // Discount: backpressure is indirect
				CausalStrength: e.CausalStrength * 0.7,
				SourceSeries:   e.TargetSeries,
				TargetSeries:   e.SourceSeries,
				Source:         phase3.EdgeSourceInferred,
				Uncertainty:    0.6,
				EvidenceBasis:  "backpressure_reversal",
			}
			reverseEdges = append(reverseEdges, rev)
		}
	}

	if len(reverseEdges) == 0 {
		return nil
	}

	// Build a subgraph with the reversed edges
	subgraph := &phase3.Graph{
		Nodes: make(map[string]*phase3.Node),
		Edges: reverseEdges,
	}

	// Copy relevant nodes into the subgraph
	if n, ok := graph.Nodes[target]; ok {
		subgraph.Nodes[target] = n
	}
	for _, e := range reverseEdges {
		if n, ok := graph.Nodes[e.From]; ok {
			subgraph.Nodes[e.From] = n
		}
	}

	// Compute combined probability from the reversed edges
	var totalProb float64
	for _, e := range reverseEdges {
		totalProb += e.ExistenceProb
	}
	prob := totalProb / float64(len(reverseEdges))

	return &phase3.CausalHypothesis{
		ID:          fmt.Sprintf("reverse_%s", target),
		Target:      target,
		Subgraph:    subgraph,
		Probability: prob,
		Mean:        prob,
		Variance:    0.1, // Higher variance — this is an inferred hypothesis
		Description: fmt.Sprintf("Backpressure hypothesis: downstream services affecting %s", target),
	}
}
