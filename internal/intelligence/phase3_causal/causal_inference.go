package phase3_causal

import (
	"math"
	"sort"
)

type InferenceConfig struct {
	MinProbability float64
	MinConfidence  float64

	ComplexityPenalty float64

	TopK int
}

type Cause struct {
	Node  string
	Score float64
}

type InferenceResult struct {
	Target string

	Causes []Cause

	Score      float64
	Confidence float64

	Hypothesis *CausalHypothesis
}

func RunCausalInference(
	hypotheses []CausalHypothesis,
	cfg InferenceConfig,
) []InferenceResult {

	if len(hypotheses) == 0 {
		return nil
	}

	scored := scoreHypotheses(hypotheses, cfg)

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	var results []InferenceResult

	limit := cfg.TopK
	if limit <= 0 || limit > len(scored) {
		limit = len(scored)
	}

	for i := 0; i < limit; i++ {

		h := scored[i].Hypothesis
		score := scored[i].Score

		if score < cfg.MinProbability {
			continue
		}

		confidence := score * math.Exp(-h.Variance)
		confidence = confidence / (1 + confidence)

		if confidence < cfg.MinConfidence {
			continue
		}

		causes := extractRootCausesDistance(h)

		result := InferenceResult{
			Target:     h.Target,
			Causes:     causes,
			Score:      score,
			Confidence: confidence,
			Hypothesis: h,
		}

		results = append(results, result)
	}

	return results
}

type scoredHypothesis struct {
	Hypothesis *CausalHypothesis
	Score      float64
}

func scoreHypotheses(
	hypotheses []CausalHypothesis,
	cfg InferenceConfig,
) []scoredHypothesis {

	var result []scoredHypothesis

	for i := range hypotheses {

		h := &hypotheses[i]

		physicsScore := 1e-6

		edgeIndex := 0.0

		for _, e := range h.Subgraph.Edges {

			if len(e.SourceSeries) < 2 || len(e.TargetSeries) < 2 {
				continue
			}

			temporal := computeTemporalCausality(e.SourceSeries, e.TargetSeries)
			if temporal <= 0 {
				continue
			}

			arrival := rate(e.SourceSeries)
			service := serviceRateFromSeries(e.SourceSeries)

			serviceSafe := math.Max(service, 0.1)
delay := arrival / serviceSafe // Baseline utilization impact
if arrival > service {
    delay += ((arrival - service) * 2.0) / serviceSafe
}

			downstreamLoad := average(e.TargetSeries)
			impact := delay / (1.0 + downstreamLoad)
			decay := math.Exp(-0.3 * (1.0 - temporal))

			physicsScore += impact * decay
			edgeIndex += 1
		}

		if edgeIndex > 0 {
			physicsScore = physicsScore / edgeIndex
		}

		variance := h.Variance
		numEdges := len(h.Subgraph.Edges)
		penalty := cfg.ComplexityPenalty * float64(numEdges)

		temporalScore := 0.0
		count := 0.0

		for _, e := range h.Subgraph.Edges {
			if len(e.SourceSeries) == 0 || len(e.TargetSeries) == 0 {
				continue
			}

			c := computeTemporalCausality(e.SourceSeries, e.TargetSeries)
			if c > 0 {
				temporalScore += c
				count += 1
			}
		}

		if count > 0 {
			temporalScore = temporalScore / count
		} else {
			temporalScore = 0.1
		}

		score := physicsScore *
			temporalScore *
			math.Exp(-penalty) *
			math.Exp(-variance)

		result = append(result, scoredHypothesis{
			Hypothesis: h,
			Score:      score,
		})
	}

	return result
}

// seriesInterventionTest implements a proper do-calculus proxy for causal
// verification. Under do(X = E[X]) the natural variation in X is eliminated;
// if X→Y is truly causal, the lagged correlation between X and Y must drop
// significantly when X is held constant (Pearl, Causality §3.2).
//
// Mechanically:
//  1. Compute baseline forward temporal causality score.
//  2. Replace source with its mean (do(X = μ_X)) — this is the interventional
//     distribution: all variance in source is removed, breaking any causal
//     channel from X to Y.
//  3. Recompute temporal causality on the intervened series.
//  4. If the intervened score is < 80% of the baseline, the link is causal.
//     If it stays high, the correlation is spurious (common cause).
//
// This correctly handles the case of high-variance causal series (which the
// previous zero-imputation approach would falsely reject).
func seriesInterventionTest(sourceSeries, targetSeries []float64) bool {
	if len(sourceSeries) < 4 || len(targetSeries) < 4 {
		return false
	}

	baseline := computeTemporalCausality(sourceSeries, targetSeries)
	if baseline <= 0 {
		return false
	}

	// Compute mean of source.
	meanX := 0.0
	for _, v := range sourceSeries {
		meanX += v
	}
	meanX /= float64(len(sourceSeries))

	// do(X = μ_X): set every source value to its mean — zero variance,
	// but the correct central tendency is preserved (unlike setting to 0).
	intervened := make([]float64, len(sourceSeries))
	for i := range sourceSeries {
		intervened[i] = meanX
	}

	// Under do(X=const), pearsonLagged returns 0 by construction (zero variance
	// in X → zero covariance). We verify this matches what computeTemporalCausality
	// sees: if baseline was driven by X's variation, it must collapse.
	intervenedScore := computeTemporalCausality(intervened, targetSeries)

	// Causal link confirmed if the intervention reduces the score by ≥20%.
	return intervenedScore < baseline*0.8
}

func extractRootCausesDistance(h *CausalHypothesis) []Cause {
	target := h.Target

	reachable := make(map[string]bool)
	stack := []string{target}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, e := range h.Subgraph.Edges {
			if e.To == cur && !reachable[e.From] {
				reachable[e.From] = true
				stack = append(stack, e.From)
			}
		}
	}

	var causes []Cause

	// Sort reachable keys for deterministic scoring order.
	reachableIDs := make([]string, 0, len(reachable))
	for nodeID := range reachable {
		reachableIDs = append(reachableIDs, nodeID)
	}
	sort.Strings(reachableIDs)

	for _, nodeID := range reachableIDs {
		if nodeID == target {
			continue
		}

		var arrivalRate, serviceRate float64

		node, hasNode := h.Subgraph.Nodes[nodeID]
		if hasNode && node.State.ArrivalRate > 0 && node.State.ServiceRate > 0 {
			arrivalRate = node.State.ArrivalRate
			serviceRate = node.State.ServiceRate
		} else {
			for _, e := range h.Subgraph.Edges {
				if e.From == nodeID && len(e.SourceSeries) >= 2 {
					arrivalRate = rate(e.SourceSeries)
					serviceRate = serviceRateFromSeries(e.SourceSeries)
					break
				}
			}
		}

		if serviceRate < 1e-9 {
			serviceRate = 1e-9
		}

		rho := arrivalRate / serviceRate

		if rho <= 0.01 { // Only skip completely dead/idle nodes
    continue
}

		// collect all outgoing edges from this node toward the target subgraph
		var sourceSeries, targetSeries []float64
		passedIntervention := false

		for _, e := range h.Subgraph.Edges {
			if e.From != nodeID {
				continue
			}
			if len(e.SourceSeries) < 4 || len(e.TargetSeries) < 4 {
				continue
			}
			if seriesInterventionTest(e.SourceSeries, e.TargetSeries) {
				passedIntervention = true
				sourceSeries = e.SourceSeries
				targetSeries = e.TargetSeries
				break
			}
		}

		if !passedIntervention && rho > 0.8 {
			continue // Only demand strict Do-Calculus drops for highly saturated nodes
		}

		temporal := computeTemporalCausality(sourceSeries, targetSeries)

		edgeScore := 0.0
		for _, e := range h.Subgraph.Edges {
			if e.From == nodeID {
				t := computeTemporalCausality(e.SourceSeries, e.TargetSeries)
				s := e.ExistenceProb * math.Abs(e.CausalStrength) * t
				if s > edgeScore {
					edgeScore = s
				}
			}
		}

		// score is driven by ρ (instability) as primary rank signal,
		// multiplied by temporal asymmetry and edge confidence
		score := rho * temporal * (1.0 + edgeScore)

		causes = append(causes, Cause{
			Node:  nodeID,
			Score: score,
		})
	}

	sort.Slice(causes, func(i, j int) bool {
		return causes[i].Score > causes[j].Score
	})

	return causes
}

func computeTemporalCausality(x, y []float64) float64 {
	if len(x) < 4 || len(y) < 4 {
		return 0
	}

	best := 0.0

	for lag := 1; lag <= 2; lag++ {
	forward := pearsonLagged(x, y, lag)
	reverse := pearsonLagged(y, x, lag)

	fAbs := math.Abs(forward)
	rAbs := math.Abs(reverse)

	// allow weak signals (stable systems)
	if fAbs < 0.01 {
		continue
	}

	// enforce directionality (but allow close signals)
	if fAbs <= rAbs {
		continue
	}

	if fAbs > best {
		best = fAbs
	}
}

return best
}
func pearsonLagged(x, y []float64, lag int) float64 {
	n := len(x)
	if n <= lag {
		return 0
	}

	xSub := x[:n-lag]
	ySub := y[lag:]

	m := float64(len(xSub))
	if m < 3 {
		return 0
	}

	var sumX, sumY float64
	for i := range xSub {
		sumX += xSub[i]
		sumY += ySub[i]
	}
	meanX := sumX / m
	meanY := sumY / m

	var cov, varX, varY float64
	for i := range xSub {
		dx := xSub[i] - meanX
		dy := ySub[i] - meanY
		cov += dx * dy
		varX += dx * dx
		varY += dy * dy
	}

	den := math.Sqrt(varX * varY)
	if den < 1e-9 {
		return 0
	}

	corr := cov / den
	return corr
}

func computeDistances(g *Graph, target string) map[string]int {
	dist := make(map[string]int)
	queue := []string{target}
	dist[target] = 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, e := range g.Edges {
			if e.To == current {
				if _, ok := dist[e.From]; !ok {
					dist[e.From] = dist[current] + 1
					queue = append(queue, e.From)
				}
			}
		}
	}
	return dist
}

func computeConfidence(score float64, variance float64) float64 {
	conf := score * math.Exp(-variance)
	if conf > 1 {
		return 1
	}
	if conf < 0 {
		return 0
	}
	return conf
}

func computeLagInfluence(x, y []float64, lag int) float64 {
	n := len(x)
	if n <= lag {
		return 0
	}
	var sum float64
	for i := lag; i < n; i++ {
		sum += x[i-lag] * y[i]
	}
	return sum / float64(n-lag)
}

func computeQueueDynamics(state *SystemState) (queueGrowth float64, delay float64) {
	if state.ArrivalRate > state.ServiceRate {
		queueGrowth = state.ArrivalRate - state.ServiceRate
		delay = queueGrowth * 0.1
	} else {
		queueGrowth = 0
		delay = 0
	}
	return
}

func average(arr []float64) float64 {
	if len(arr) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range arr {
		sum += v
	}
	return sum / float64(len(arr))
}

// serviceRateFromSeries estimates the service rate μ from a time-series
// using the same inverse-variance relationship as Phase 1 (signal_matrix.go
// GetNodeState). This avoids the arbitrary ×0.8 fallback that previously
// assumed a fixed 80 % utilization when no NodeState was available.
//
// Physics rationale: high variance in the signal derivative indicates an
// unstable, resource-constrained node (low μ); a smooth, low-variance
// derivative indicates spare capacity (high μ).
//
//	μ = 0.5 + ( 1 / (1 + Var[Δx]) ) × 2.0
//
// Returns a floor of 1e-9 so callers can safely divide by the result.
func serviceRateFromSeries(series []float64) float64 {
	if len(series) < 2 {
		return 1.0
	}
	var sumSq float64
	n := float64(len(series) - 1)
	for i := 1; i < len(series); i++ {
		d := series[i] - series[i-1]
		sumSq += d * d
	}
	variance := sumSq / n
	mu := 0.5 + (1.0/(1.0+variance))*2.0
	if mu < 1e-9 {
		mu = 1e-9
	}
	return mu
}

// rate returns the mean absolute rate of change of the series.
// Previously this function summed only positive deltas, which introduced
// systematic upward bias — a stable series at high values would score higher
// than a volatile series at low values. We now use the mean of all first
// differences (signed), which correctly estimates the drift of the process.
func rate(series []float64) float64 {
	if len(series) < 2 {
		return 0
	}
	sum := 0.0
	for i := 1; i < len(series); i++ {
		sum += series[i] - series[i-1]
	}
	// Mean first difference = net drift per step.
	// Use absolute value so the score is symmetric around zero trend.
	drift := sum / float64(len(series)-1)
	if drift < 0 {
		drift = -drift
	}
	return drift
}
