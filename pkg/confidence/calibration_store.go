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

	// Phase 4: Component values that produced this confidence (Bayesian, Causal, Physics, Telemetry)
	Components []float64 `json:"components,omitempty"`
}

// CalibrationStore persists and analyzes historical accuracy vs confidence.
type CalibrationStore struct {
	mu      sync.RWMutex
	records map[string]FeedbackRecord

	// Learned weights for components [Bayesian, Causal, Physics, Telemetry]
	learnedWeights []float64
}

// NewCalibrationStore creates a new empty CalibrationStore.
func NewCalibrationStore() *CalibrationStore {
	return &CalibrationStore{
		records:        make(map[string]FeedbackRecord),
		learnedWeights: []float64{0.25, 0.25, 0.25, 0.25}, // Initial uniform weights
	}
}

// RecordFeedback stores a human feedback event and updates the learned weights via SGD.
func (s *CalibrationStore) RecordFeedback(record FeedbackRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}
	s.records[record.PredictionID] = record

	// Online Calibration (SGD Step)
	// We want to minimize the squared error between PredictedConfidence and Actual Outcome.
	// Actual outcome = 1.0 if correct, 0.0 if incorrect.
	if len(record.Components) == 4 {
		outcome := 0.0
		if record.PredictedRootCause == record.ConfirmedRootCause {
			outcome = 1.0
		}
		
		errorSignal := record.PredictedConfidence - outcome
		learningRate := 0.01

		sum := 0.0
		for i := 0; i < 4; i++ {
			// Gradient of squared error w.r.t weight[i] is roughly errorSignal * component[i]
			s.learnedWeights[i] -= learningRate * errorSignal * record.Components[i]
			if s.learnedWeights[i] < 0.05 {
				s.learnedWeights[i] = 0.05 // Floor to prevent zeroing out
			}
			sum += s.learnedWeights[i]
		}
		
		// Normalize weights so they sum to 1.0
		for i := 0; i < 4; i++ {
			s.learnedWeights[i] /= sum
		}
	}
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

// GetLearnedWeights returns the calibrated weights for confidence components.
func (s *CalibrationStore) GetLearnedWeights() []float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	// Copy to avoid race
	w := make([]float64, 4)
	copy(w, s.learnedWeights)
	return w
}

// CalibrationReport contains diagnostics for production validation.
type CalibrationReport struct {
	ECE                   float64 `json:"expected_calibration_error"`
	BrierScore            float64 `json:"brier_score"`
	TotalSamples          int     `json:"total_samples"`
	CalibrationTrendSlope float64 `json:"calibration_trend_slope"`
	LearnedWeights        []float64 `json:"learned_weights"`
}

// GenerateReport produces the full calibration diagnostics.
func (s *CalibrationStore) GenerateReport() CalibrationReport {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return CalibrationReport{
		ECE:          s.ComputeECE(10), // Using 10 bins
		BrierScore:   s.ComputeBrierScore(),
		TotalSamples: len(s.records),
		CalibrationTrendSlope: 0.0, // Stub for drift detection
		LearnedWeights: s.GetLearnedWeights(),
	}
}
