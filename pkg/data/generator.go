// Package data provides the Dataset and DataPoint types used by the pipeline
// to represent time-series metrics, and helpers for extracting and correlating
// series.
//
// ALL synthetic data generators that previously existed here have been removed.
// The pipeline requires real container metrics from the metrics store.
// If no real data is available the API returns a clear "waiting for data" error.
// Tests that need deterministic datasets should build them directly in _test.go files.
package data

import "math"

// DataPoint is one time-step across all monitored nodes.
type DataPoint struct {
	Timestamp float64
	Values    map[string]float64
	Missing   map[string]bool
}

// Dataset is the ordered time-series fed into the causal pipeline.
// Nodes lists node IDs; Points are chronological.
type Dataset struct {
	Points []DataPoint
	Nodes  []string
}

// ExtractTimeSeries returns nodeName values across all time-steps,
// forward-filling (LOCF) across gaps marked Missing.
func ExtractTimeSeries(dataset *Dataset, nodeName string) []float64 {
	series := make([]float64, 0, len(dataset.Points))
	lastValue := 0.0
	hasValue := false
	for _, point := range dataset.Points {
		if !point.Missing[nodeName] {
			series = append(series, point.Values[nodeName])
			lastValue = point.Values[nodeName]
			hasValue = true
		} else if hasValue {
			series = append(series, lastValue)
		} else {
			series = append(series, 0.0)
		}
	}
	return series
}

// EstimateCorrelation computes the Pearson correlation between two series.
// Used by the causal graph builder for edge weight initialisation.
func EstimateCorrelation(series1, series2 []float64) float64 {
	if len(series1) != len(series2) || len(series1) == 0 {
		return 0
	}
	mean1, mean2 := 0.0, 0.0
	for i := range series1 {
		mean1 += series1[i]
		mean2 += series2[i]
	}
	n := float64(len(series1))
	mean1 /= n
	mean2 /= n
	cov, var1, var2 := 0.0, 0.0, 0.0
	for i := range series1 {
		d1 := series1[i] - mean1
		d2 := series2[i] - mean2
		cov += d1 * d2
		var1 += d1 * d1
		var2 += d2 * d2
	}
	if var1 == 0 || var2 == 0 {
		return 0
	}
	return cov / math.Sqrt(var1*var2)
}