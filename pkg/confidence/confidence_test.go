package confidence

import (
	"fmt"
	"testing"
)

func TestMonteCarloAdaptiveConvergence(t *testing.T) {
	engine := NewMCEngine()
	means := []float64{0.8, 0.8, 0.8, 0.8}
	weights := []float64{0.25, 0.25, 0.25, 0.25}
	L := BuildCholesky(0.01)

	output := engine.RunAdaptive(means, L, weights, 5000, 0.01)

	// It should converge before 5000 samples typically due to high tolerance (0.01)
	if output.SampleCount > 4000 {
		t.Logf("Expected early convergence, but took %d samples", output.SampleCount)
	}

	if output.Mean < 0.7 || output.Mean > 0.9 {
		t.Errorf("Expected mean around 0.8, got %f", output.Mean)
	}

	if output.P05 >= output.Mean || output.P95 <= output.Mean {
		t.Errorf("Expected P05 < Mean < P95, got P05=%f, Mean=%f, P95=%f", output.P05, output.Mean, output.P95)
	}
}

func TestCalibrationScores(t *testing.T) {
	store := NewCalibrationStore()

	// Perfect calibration: confidence matches accuracy
	store.RecordFeedback(FeedbackRecord{PredictionID: "1", PredictedRootCause: "A", ConfirmedRootCause: "A", PredictedConfidence: 1.0})
	store.RecordFeedback(FeedbackRecord{PredictionID: "2", PredictedRootCause: "B", ConfirmedRootCause: "B", PredictedConfidence: 1.0})
	store.RecordFeedback(FeedbackRecord{PredictionID: "3", PredictedRootCause: "C", ConfirmedRootCause: "D", PredictedConfidence: 0.0})

	ece := store.ComputeECE(10)
	if ece > 0.1 {
		t.Errorf("Expected low ECE for perfect calibration, got %f", ece)
	}

	brier := store.ComputeBrierScore()
	if brier > 0.1 {
		t.Errorf("Expected low Brier score, got %f", brier)
	}

	// Terrible calibration: highly confident but wrong
	store2 := NewCalibrationStore()
	store2.RecordFeedback(FeedbackRecord{PredictionID: "1", PredictedRootCause: "A", ConfirmedRootCause: "Z", PredictedConfidence: 1.0})
	store2.RecordFeedback(FeedbackRecord{PredictionID: "2", PredictedRootCause: "B", ConfirmedRootCause: "Y", PredictedConfidence: 1.0})

	ece2 := store2.ComputeECE(10)
	if ece2 < 0.9 {
		t.Errorf("Expected high ECE for terrible calibration, got %f", ece2)
	}

	brier2 := store2.ComputeBrierScore()
	if brier2 < 0.9 {
		t.Errorf("Expected high Brier score, got %f", brier2)
	}
}

func TestConformalPredictionCoverage(t *testing.T) {
	store := NewCalibrationStore()
	engine := NewConformalEngine(store)

	// Add calibration data: 9 cases where top score is true, 1 case where it isn't
	for i := 0; i < 9; i++ {
		store.RecordFeedback(FeedbackRecord{
			PredictionID:        fmt.Sprintf("id-%d", i),
			PredictedRootCause:  "A",
			ConfirmedRootCause:  "A",
			PredictedConfidence: 0.9,
		})
	}
	store.RecordFeedback(FeedbackRecord{
		PredictionID:        "id-error",
		PredictedRootCause:  "A",
		ConfirmedRootCause:  "B",
		PredictedConfidence: 0.9, // Overconfident error
	})

	scores := map[string]float64{
		"A": 0.95,
		"B": 0.80,
		"C": 0.10,
	}

	// Request 90% coverage
	set := engine.PredictionSet(scores, 0.1)

	// Should contain A and B based on tau calculation
	foundB := false
	for _, c := range set {
		if c == "B" {
			foundB = true
		}
	}

	if !foundB {
		t.Errorf("Expected set to include 'B' to satisfy 90%% coverage given calibration errors, got set: %v", set)
	}
}
