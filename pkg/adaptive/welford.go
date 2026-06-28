package adaptive

import (
	"math"
	"sync"
)

// WelfordState maintains online variance calculation.
// Citation: Welford (1962) "Note on a method for calculating corrected sums of squares and products"
type WelfordState struct {
	mu    sync.RWMutex
	count int64
	mean  float64
	m2    float64
}

// Update adds a new observation.
func (w *WelfordState) Update(val float64) {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.count++
	
	// Outlier robustness: clamp extreme spikes if we have a stable baseline
	if w.count > 10 {
		var stddev float64
		if w.count >= 2 {
			stddev = math.Sqrt(w.m2 / float64(w.count-1))
		}
		if stddev > 0 {
			maxDelta := 5.0 * stddev
			if val > w.mean+maxDelta {
				val = w.mean + maxDelta
			} else if val < w.mean-maxDelta {
				val = w.mean - maxDelta
			}
		}
	}
	
	delta := val - w.mean
	w.mean += delta / float64(w.count)
	delta2 := val - w.mean
	w.m2 += delta * delta2
}

// Mean returns the running mean.
func (w *WelfordState) Mean() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.mean
}

// StdDev returns the running sample standard deviation.
func (w *WelfordState) StdDev() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.count < 2 {
		return 0.0
	}
	return math.Sqrt(w.m2 / float64(w.count-1))
}

// Count returns the number of observations.
func (w *WelfordState) Count() int64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.count
}
