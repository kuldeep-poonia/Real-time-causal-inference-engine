package confidence

import (
	"sync"
	"time"
	"math"
)

// FeedbackRecord represents a single human-verified incident response.
type FeedbackRecord struct {
	PredictionID         string    `json:"prediction_id"`
	PredictedRootCause   string    `json:"predicted_root_cause"`
	ConfirmedRootCause   string    `json:"confirmed_root_cause"`
	PredictedConfidence  float64   `json:"predicted_confidence"`
	OperatorAction       string    `json:"operator_action"`
	IncidentClosed       bool      `json:"incident_closed"`
	Timestamp            time.Time `json:"timestamp"`
}

// CalibrationStore persists and analyzes historical accuracy vs confidence.
type CalibrationStore struct {
	mu      sync.RWMutex
	records map[string]FeedbackRecord
}

// NewCalibrationStore creates a new empty CalibrationStore.
func NewCalibrationStore() *CalibrationStore {
	return &CalibrationStore{
		records: make(map[string]FeedbackRecord),
	}
}

// RecordFeedback stores a human feedback event.
func (s *CalibrationStore) RecordFeedback(record FeedbackRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}
	s.records[record.PredictionID] = record
}

// ComputeECE calculates the Expected Calibration Error based on stored feedback.
// It groups predictions into bins and computes the difference between 
// mean predicted confidence and actual accuracy in each bin.
func (s *CalibrationStore) ComputeECE(numBins int) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.records) == 0 {
		return 0.0
	}

	bins := make([]struct {
		TotalConf float64
		Hits      int
		Count     int
	}, numBins)

	for _, rec := range s.records {
		binIdx := int(rec.PredictedConfidence * float64(numBins))
		if binIdx >= numBins {
			binIdx = numBins - 1
		}

		bins[binIdx].TotalConf += rec.PredictedConfidence
		bins[binIdx].Count++
		if rec.PredictedRootCause == rec.ConfirmedRootCause {
			bins[binIdx].Hits++
		}
	}

	var ece float64
	totalSamples := float64(len(s.records))

	for _, bin := range bins {
		if bin.Count > 0 {
			meanConf := bin.TotalConf / float64(bin.Count)
			accuracy := float64(bin.Hits) / float64(bin.Count)
			weight := float64(bin.Count) / totalSamples
			
			ece += weight * math.Abs(meanConf - accuracy)
		}
	}

	return ece
}

// ComputeBrierScore calculates the Brier score (mean squared error of probability predictions).
func (s *CalibrationStore) ComputeBrierScore() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.records) == 0 {
		return 0.0
	}

	var sumSq float64
	for _, rec := range s.records {
		outcome := 0.0
		if rec.PredictedRootCause == rec.ConfirmedRootCause {
			outcome = 1.0
		}
		diff := rec.PredictedConfidence - outcome
		sumSq += diff * diff
	}

	return sumSq / float64(len(s.records))
}
