package orchestrator

import (
	"fmt"
	"log"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"absia/pkg/bridge"
	"absia/pkg/data"
	"absia/pkg/metricsstore"
	"absia/pkg/policy"

	phase2 "absia/internal/intelligence/phase2_pattern"
	phase3 "absia/internal/intelligence/phase3_causal"
	phase4 "absia/internal/intelligence/phase4_explanation"
	phase5 "absia/internal/intelligence/phase5_insight"
)

// ============================================================================
// RESULT TYPES
// ============================================================================

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

	// Safety gate — mandatory, always evaluated
	SafetyResult *SafetyResult

	ExecutionTimeMS   float64 // populated for every run
	ErrorsEncountered []string
	DataSource        string // "real" | "synthetic"
}

// ============================================================================
// PACKAGE-LEVEL CONFIGURATION
// ============================================================================

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

// ============================================================================
// PER-TARGET RANKING STATE
// Fix: was a single global slice, causing concurrent requests for different
// targets to corrupt each other's ranking instability signal.
// Now keyed by target node ID so each target has an independent prior.
// ============================================================================

var (
	prevRankingMu       sync.RWMutex
	prevRankingByTarget map[string][]string // keyed by target node ID
)

func init() {
	prevRankingByTarget = make(map[string][]string)
}

// GetPrevRanking returns a copy of the last stored cause ranking for target.
// Returns nil on the first call for a given target — correct first-call
// behaviour for lgRankInstability which returns 0.0 when prev is nil.
func GetPrevRanking(target string) []string {
	prevRankingMu.RLock()
	defer prevRankingMu.RUnlock()
	src := prevRankingByTarget[target]
	if len(src) == 0 {
		return nil
	}
	cp := make([]string, len(src))
	copy(cp, src)
	return cp
}

// setPrevRanking stores the current cause ranking for the given target.
func setPrevRanking(target string, causes []string) {
	prevRankingMu.Lock()
	defer prevRankingMu.Unlock()
	dst := make([]string, len(causes))
	copy(dst, causes)
	prevRankingByTarget[target] = dst
}

// ============================================================================
// PUBLIC ENTRY POINTS
// ============================================================================

// ExecuteFullPipeline keeps backward compatibility for tests.
// Calls ExecuteFullPipelineFromStore with a nil store (synthetic data).
func ExecuteFullPipeline(arrivalRate, serviceRate, queueLength float64) (*PipelineResult, error) {
	return ExecuteFullPipelineFromStore(arrivalRate, serviceRate, queueLength, nil)
}

// ExecuteFullPipelineFromStore is the production entry point.
// When store is non-nil and HasRealData() is true, real Prometheus/ingested
// metrics flow through the pipeline. Otherwise it falls back to the
// synthetic dataset so the API is always functional.
func ExecuteFullPipelineFromStore(
	arrivalRate, serviceRate, queueLength float64,
	store *metricsstore.Store,
) (*PipelineResult, error) {

	startTime := time.Now() // Fix 5: record start for ExecutionTimeMS

	result := &PipelineResult{ErrorsEncountered: make([]string, 0)}

	if serviceRate <= 0 {
		return nil, fmt.Errorf("serviceRate must be > 0, got %f", serviceRate)
	}
	if arrivalRate < 0 {
		return nil, fmt.Errorf("arrivalRate must be >= 0, got %f", arrivalRate)
	}
	if queueLength < 0 {
		return nil, fmt.Errorf("queueLength must be >= 0, got %f", queueLength)
	}

	log.Println("[ORCHESTRATOR] Starting full production pipeline...")

	seed := getSeed()
	ps := getPolicyStore()

	// ========================================================================
	// DATA SOURCE
	// ========================================================================
	var realisticData *data.Dataset
	useRealData := store != nil && store.HasRealData()

	if useRealData {
		log.Println("[ORCHESTRATOR] Using REAL metrics from store")
		result.DataSource = "real"
		realisticData = convertStoreToDataset(store)
		if realisticData == nil || len(realisticData.Points) < 4 {
			log.Println("[ORCHESTRATOR] Store data insufficient, falling back to synthetic")
			useRealData = false
		}
	}
	if !useRealData {
		log.Println("[ORCHESTRATOR] Using SYNTHETIC metrics")
		result.DataSource = "synthetic"
		realisticData = data.GenerateRealisticCausalDataWithSeed(arrivalRate, 50, 0.5, 0.0, seed)
	}

	log.Printf("  -> Dataset: nodes=%v timesteps=%d source=%s",
		realisticData.Nodes, len(realisticData.Points), result.DataSource)

	// ========================================================================
	// PHASE 1: SIGNAL PHYSICS
	// ========================================================================
	log.Println("[ORCHESTRATOR] Phase 1: signal physics...")

	var p1AR, p1SR, p1QL float64
	if useRealData && len(realisticData.Nodes) > 0 {
		primaryNode := targetNodeOrDefault(realisticData.Nodes)
		if store != nil {
    if sample, ok := store.GetLatestSample(primaryNode); ok {
			p1AR = sample.ArrivalRate
			p1SR = sample.ServiceRate
			p1QL = sample.QueueLength
		}
	}
	}
	if p1SR <= 0 {
		p1AR = arrivalRate
		p1SR = serviceRate
		p1QL = queueLength
	}
	if p1SR <= 0 {
    p1SR = serviceRate
}
p1Load := p1AR / p1SR
	result.Phase1NodeState = &phase1NodeState{
		Load: p1Load, ArrivalRate: p1AR, ServiceRate: p1SR,
		QueueLength: p1QL, ProcessingDelay: p1QL / p1SR, Timestamp: 0,
	}
	log.Printf("  -> rho=%.3f lambda=%.2f mu=%.2f", p1Load, p1AR, p1SR)

	// ========================================================================
	// PHASE 2: FULL PATTERN DETECTION
	// ========================================================================
	log.Println("[ORCHESTRATOR] Phase 2: full pattern detection...")

	signalMatrix := buildSignalMatrix(realisticData)

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

	// ========================================================================
	// PHASE 3: CAUSAL GRAPH DISCOVERY + FULL CAUSAL ENGINE
	// ========================================================================
	log.Println("[ORCHESTRATOR] Phase 3: causal graph + full causal engine...")

	temporalGraph := buildTemporalGraph(realisticData)

	probCfg := phase3.ProbabilityConfig{
		MinSamples: 4, LagSteps: []int{1, 2},
		TimeTolerance: 30 * time.Second, DirectionThreshold: 0.1,
	}
	discoveredGraph := phase3.UpdateGraphProbabilities(temporalGraph, probCfg)
	log.Printf("  -> discovered: %d nodes %d edges",
		len(discoveredGraph.Nodes), len(discoveredGraph.Edges))

	phase3.AssignTimestampsFromTopologicalOrder(discoveredGraph)

	for _, nodeID := range realisticData.Nodes {
		series := data.ExtractTimeSeries(realisticData, nodeID)
		if node, ok := discoveredGraph.Nodes[nodeID]; ok {
			node.Series = series
			var ns phase3.NodeState
			if useRealData {
				if sample, ok := store.GetLatestSample(nodeID); ok {
					sr := sample.ServiceRate
					if sr <= 0 {
						sr = serviceRate
					}
					ns = phase3.NodeState{
						ArrivalRate: sample.ArrivalRate, ServiceRate: sr,
						Load: sample.ArrivalRate / sr, QueueLength: sample.QueueLength,
						Timestamp: node.State.Timestamp,
					}
				}
			}
			if ns.ServiceRate <= 0 {
				ns = phase3.NodeState{
					ArrivalRate: arrivalRate, ServiceRate: serviceRate,
					Load: arrivalRate / serviceRate, QueueLength: queueLength,
					Timestamp: node.State.Timestamp,
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

	hypotheses := phase3.BuildCausalHypotheses(discoveredGraph,
		phase3.CausalBuilderConfig{MinProbability: 0.1, MinStrength: 0.05, MaxCauses: 5})
	log.Printf("  -> hypotheses pre-filter: %d", len(hypotheses))

	if len(hypotheses) == 0 {
		result.ErrorsEncountered = append(result.ErrorsEncountered,
			"Phase 3: no causal hypotheses — system may be stationary")
		result.SafetyResult = evaluateSafetyGateEmpty(targetNodeOrDefault(realisticData.Nodes))
		result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6
		return result, nil
	}

	dsepEnrichment := RunDSeparationEnrichment(discoveredGraph)
	backdoorEnrichment := RunBackdoorEnrichment(discoveredGraph, temporalGraph, dsepEnrichment)
	log.Printf("  -> d-sep enrichment: %d confirmed %d confounded edges",
		len(dsepEnrichment.DSepConfirmed), len(dsepEnrichment.ConfoundedPairs))

	for key, effect := range backdoorEnrichment.Effects {
		for _, e := range discoveredGraph.Edges {
			if e.From+"->"+e.To == key {
				if effect != 0 {
					e.CausalStrength = effect
				}
				break
			}
		}
	}

	targetNodeID := mostDownstreamNode(discoveredGraph, realisticData.Nodes)
	hypotheses = applyDSeparationFilter(hypotheses, discoveredGraph)
	log.Printf("  -> D-sep filter: %d hypotheses survive", len(hypotheses))

	for i := range hypotheses {
		if hypotheses[i].Target == "" {
			hypotheses[i].Target = targetNodeID
		}
	}

	phase3Results := phase3.RunCausalInference(hypotheses,
		phase3.InferenceConfig{MinProbability: 0.0, MinConfidence: 0.0, TopK: 1})

	if len(phase3Results) > 0 && len(phase3Results[0].Causes) > 0 {
		result.Phase3Result = &phase3Results[0]
		result.Phase3Graph = discoveredGraph
		log.Printf("  -> root cause: target=%s score=%.4f conf=%.4f",
			phase3Results[0].Target, phase3Results[0].Score, phase3Results[0].Confidence)
	} else {
		result.ErrorsEncountered = append(result.ErrorsEncountered,
			"Phase 3: no causal drivers (load may be below threshold)")
		result.SafetyResult = evaluateSafetyGateEmpty(targetNodeID)
		result.ExecutionTimeMS = float64(time.Since(startTime).Nanoseconds()) / 1e6
		return result, nil
	}

	if len(phase3Results[0].Causes) > 0 {
		backdoorEffects := make(map[string]float64)
		for _, cause := range phase3Results[0].Causes {
			br := phase3.ComputeBackdoorEffect(discoveredGraph, temporalGraph,
				cause.Node, targetNodeID)
			backdoorEffects[cause.Node] = br.Effect
		}
		result.BackdoorEffects = backdoorEffects
		log.Printf("  -> backdoor effects: %v", backdoorEffects)
	}

	nodeStatesMap := buildNodeStatesMap(discoveredGraph)
	physicsRoots := phase3.FindRootCauseByPropagation(discoveredGraph, nodeStatesMap, targetNodeID)
	result.PhysicsRootCauses = physicsRoots
	if len(physicsRoots) > 0 {
		log.Printf("  -> physics root: %s score=%.4f", physicsRoots[0].NodeID, physicsRoots[0].Score)
	}

	// ========================================================================
	// PHASE 4: EXPLANATION
	// ========================================================================
	log.Println("[ORCHESTRATOR] Phase 4: explanation generation...")

	phase4Graph := bridge.ConvertPhase3ResultToPhase4Graph(*result.Phase3Result, result.Phase3Graph)
	phase4Dataset := bridge.ConvertPhase3ResultToPhase4Dataset(result.Phase3Graph)

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

	// ========================================================================
	// PHASE 5: RL POLICY + SAFETY GATE
	// Fix 4: warmstart from persisted weights; persist after training.
	// ========================================================================
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

	policy5 := phase5.WarmstartTrain(priorPolicy, phase5Graph, beliefState, phase5Exp, actions, targetNodeID, 100, seed)
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

	result.SafetyResult = evaluateSafetyGateFull(phase5Graph, phase5Exp, result.Phase3Result, targetNodeID, prevRanking)

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

// ============================================================================
// SAFETY GATE
// ============================================================================

func evaluateSafetyGateFull(
	graph *phase5.CausalGraph,
	exp phase5.Explanation,
	phase3Result *phase3.InferenceResult,
	target string,
	prevRanking []string,
) *SafetyResult {
	p3Root := ""
	if phase3Result != nil && len(phase3Result.Causes) > 0 {
		p3Root = phase3Result.Causes[0].Node
	}
	report := RunSafetyGate(graph, exp, prevRanking, target)
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
	conf := phase5.ComputeConfidence(emptyFusion, nil, empty, latent)
	fallback := phase5.EvaluateFallback(conf, latent, emptyFusion, nil, target)
	return &SafetyResult{LatentRisk: latent, Confidence: conf, Fallback: fallback, Fusion: emptyFusion}
}

// ============================================================================
// D-SEPARATION FILTER
// ============================================================================

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

// ============================================================================
// DATA HELPERS
// ============================================================================

func convertStoreToDataset(store *metricsstore.Store) *data.Dataset {
	nodeIDs := store.GetAllNodeIDs()
	if len(nodeIDs) == 0 {
		return nil
	}

	// Only include nodes that have enough samples to contribute meaningfully.
	// Nodes with fewer than 4 samples are excluded rather than truncating
	// the entire dataset to their shorter length.
	qualified := make([]string, 0, len(nodeIDs))
	maxLen := 0
	for _, id := range nodeIDs {
		s := store.GetArrivalRateSeries(id)
		if len(s) >= 4 {
			qualified = append(qualified, id)
			if len(s) > maxLen {
				maxLen = len(s)
			}
		}
	}

	// Need at least 2 nodes with real data for causal graph to be meaningful.
	// A single node produces a trivial graph with no causal structure.
	if len(qualified) < 2 || maxLen < 4 {
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
				// Forward-fill: use the last known value for nodes with fewer samples.
				// This is more accurate than marking as missing and avoids shrinking the dataset.
				ds.Points[t].Values[id] = series[len(series)-1]
			}
		}
	}
	return ds
}

func assignTopologicalTimestamps(graph *phase3.Graph, nodeIDs []string) map[string]float64 {
	inDeg := make(map[string]int)
	for _, id := range nodeIDs {
		inDeg[id] = 0
	}
	for _, e := range graph.Edges {
		inDeg[e.To]++
	}
	queue := make([]string, 0)
	for _, id := range nodeIDs {
		if inDeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	timestamps := make(map[string]float64, len(nodeIDs))
	tick := 0.0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		timestamps[cur] = tick
		tick++
		var nexts []string
		for _, e := range graph.Edges {
			if e.From == cur {
				inDeg[e.To]--
				if inDeg[e.To] == 0 {
					nexts = append(nexts, e.To)
				}
			}
		}
		sort.Strings(nexts)
		queue = append(queue, nexts...)
	}
	for _, id := range nodeIDs {
		if _, ok := timestamps[id]; !ok {
			timestamps[id] = tick
			tick++
		}
	}
	return timestamps
}

func buildNodeStatesMap(graph *phase3.Graph) map[string]phase3.NodeState {
	states := make(map[string]phase3.NodeState, len(graph.Nodes))
	for id, node := range graph.Nodes {
		states[id] = node.State
	}
	return states
}

func targetNodeOrDefault(nodes []string) string {
	if len(nodes) > 0 {
		return nodes[len(nodes)-1]
	}
	return "target"
}

// ============================================================================
// SIGNAL / MATRIX HELPERS
// ============================================================================

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
	tg := &phase3.TemporalGraph{
		Nodes: make(map[string]*phase3.TemporalSeries),
		Edges: make([]*phase3.TemporalEdge, 0),
	}

	const stepSeconds = 15
	epoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, nodeID := range dataset.Nodes {
		series := &phase3.TemporalSeries{
			Points: make([]phase3.TemporalPoint, 0, len(dataset.Points)),
		}
		stepIdx := 0
		for _, point := range dataset.Points {
			if point.Missing[nodeID] {
				stepIdx++
				continue
			}
			t := epoch.Add(time.Duration(stepIdx) * stepSeconds * time.Second)
			tn := &phase3.TemporalNode{
				NodeID:     nodeID,
				Time:       t,
				Value:      point.Values[nodeID],
				Noise:      0.1,
				Intensity:  1.0,
				Confidence: 1.0,
			}
			series.Points = append(series.Points, phase3.TemporalPoint{
				Time: t,
				Node: tn,
			})
			stepIdx++
		}
		tg.Nodes[nodeID] = series
	}
	return tg
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

// ============================================================================
// SUMMARY
// ============================================================================

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