package telemetry

import (
	"expvar"
	"strconv"
	"sync"
)

// ExpvarTracker implements Tracker using the standard library expvar package.
type ExpvarTracker struct {
	mu           sync.Mutex
	counters     map[string]*expvar.Int
	avgDurations map[string]*avgExpvar
}

// NewExpvarTracker creates a new ExpvarTracker.
func NewExpvarTracker() *ExpvarTracker {
	return &ExpvarTracker{
		counters:     make(map[string]*expvar.Int),
		avgDurations: make(map[string]*avgExpvar),
	}
}

func (e *ExpvarTracker) Increment(metric string) {
	e.Add(metric, 1)
}

func (e *ExpvarTracker) Add(metric string, value int64) {
	e.mu.Lock()
	counter, ok := e.counters[metric]
	if !ok {
		counter = expvar.NewInt(metric)
		e.counters[metric] = counter
	}
	e.mu.Unlock()
	counter.Add(value)
}

func (e *ExpvarTracker) RecordTime(metric string, durationMS float64) {
	e.mu.Lock()
	avg, ok := e.avgDurations[metric]
	if !ok {
		avg = &avgExpvar{}
		expvar.Publish(metric, avg)
		e.avgDurations[metric] = avg
	}
	e.mu.Unlock()
	avg.Add(durationMS)
}

// avgExpvar tracks a running average for expvar publication.
type avgExpvar struct {
	mu    sync.RWMutex
	sum   float64
	count int64
}

func (a *avgExpvar) Add(val float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sum += val
	a.count++
}

func (a *avgExpvar) String() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.count == 0 {
		return "0.0"
	}
	avg := a.sum / float64(a.count)
	// format as string per expvar.Var interface requirements
	return float2string(avg)
}

func float2string(f float64) string {
	return strconv.FormatFloat(f, 'f', 2, 64)
}
