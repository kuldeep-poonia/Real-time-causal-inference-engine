package confidence

import (
	"sort"
	"math"
)

// ConformalEngine provides distribution-free prediction sets.
type ConformalEngine struct {
	store *CalibrationStore
}

// NewConformalEngine creates a new conformal engine.
func NewConformalEngine(store *CalibrationStore) *ConformalEngine {
	return &ConformalEngine{store: store}
}

// PredictionSet returns a set of possible root causes that contains the true root cause
// with probability at least 1-alpha, assuming exchangeability with historical data.
// scores is a map of cause -> heuristic score.
func (e *ConformalEngine) PredictionSet(scores map[string]float64, alpha float64) []string {
	e.store.mu.RLock()
	records := make([]FeedbackRecord, 0, len(e.store.records))
	for _, r := range e.store.records {
		records = append(records, r)
	}
	e.store.mu.RUnlock()

	// If we don't have enough calibration data, fallback to naive top-k heuristic
	if len(records) < 10 {
		return naivePredictionSet(scores, alpha)
	}

	// 1. Compute non-conformity scores for calibration set
	// non-conformity = 1.0 - predicted_probability_of_true_label
	var nonConformityScores []float64
	for _, rec := range records {
		probTrueLabel := 0.0
		if rec.PredictedRootCause == rec.ConfirmedRootCause {
			probTrueLabel = rec.PredictedConfidence
		}
		nc := 1.0 - probTrueLabel
		nonConformityScores = append(nonConformityScores, nc)
	}
	sort.Float64s(nonConformityScores)

	// 2. Find the (1-alpha) quantile of the non-conformity scores
	n := float64(len(nonConformityScores))
	qIdx := int(math.Ceil((n + 1) * (1.0 - alpha)))
	if qIdx >= len(nonConformityScores) {
		qIdx = len(nonConformityScores) - 1
	}
	tau := nonConformityScores[qIdx]

	// 3. Construct prediction set for the new query
	var set []string
	for cause, score := range scores {
		// New non-conformity score if this cause were the true cause
		nc := 1.0 - score
		if nc <= tau {
			set = append(set, cause)
		}
	}

	// Safety fallback: if set is empty (e.g. all scores are very low), include the argmax
	if len(set) == 0 {
		var best string
		maxScore := -1.0
		for c, s := range scores {
			if s > maxScore {
				maxScore = s
				best = c
			}
		}
		if best != "" {
			set = append(set, best)
		}
	}

	return set
}

func naivePredictionSet(scores map[string]float64, alpha float64) []string {
	// Simple heuristic: sort by score, take top N that sum to 1-alpha, or just top 1 if confidence > 1-alpha
	type kv struct {
		k string
		v float64
	}
	var arr []kv
	for k, v := range scores {
		arr = append(arr, kv{k, v})
	}
	sort.Slice(arr, func(i, j int) bool {
		return arr[i].v > arr[j].v
	})

	var set []string
	sum := 0.0
	for _, item := range arr {
		set = append(set, item.k)
		sum += item.v
		if sum >= (1.0 - alpha) {
			break
		}
	}
	if len(set) == 0 && len(arr) > 0 {
		set = append(set, arr[0].k)
	}
	return set
}
