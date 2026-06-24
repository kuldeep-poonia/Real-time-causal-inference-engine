package phase1_signal

import (
	"math"
	"sync"
	"time"
)



type Aggregator struct {
	mu sync.Mutex

	schema *SignalSchema
	manager *Manager

	interval float64 // seconds

	// tick → group → features
	buffer map[int64]map[string]map[string]float64

	// last known values (carry forward)
	last map[string]map[string]float64

	maxTicks int
}

/*
init
*/
func NewAggregator(schema *SignalSchema, m *Manager, interval float64) *Aggregator {
	return &Aggregator{
		schema:   schema,
		manager:  m,
		interval: interval,
		buffer:   make(map[int64]map[string]map[string]float64),
		last:     make(map[string]map[string]float64),
		maxTicks: 100, // simple limit
	}
}

/*
time align → tick
*/
func (a *Aggregator) align(t float64) int64 {
	return int64(math.Round(t / a.interval))
}

/*
add incoming metric
*/
func (a *Aggregator) Add(groupID string, feature string, value float64, t float64) {

	a.mu.Lock()
	defer a.mu.Unlock()

	// schema check
	if _, ok := a.schema.index[feature]; !ok {
		return
	}

	tick := a.align(t)

	if _, ok := a.buffer[tick]; !ok {
		a.buffer[tick] = make(map[string]map[string]float64)
	}

	if _, ok := a.buffer[tick][groupID]; !ok {
		a.buffer[tick][groupID] = make(map[string]float64)
	}

	a.buffer[tick][groupID][feature] = value

	// save last value
	if _, ok := a.last[groupID]; !ok {
		a.last[groupID] = make(map[string]float64)
	}
	a.last[groupID][feature] = value
}

/*
flush one tick
*/
func (a *Aggregator) flushTick(tick int64) {

	groups, ok := a.buffer[tick]
	if !ok {
		return
	}

	for groupID, features := range groups {

		full := make(map[string]float64)

		// build full vector (schema based)
		for _, f := range a.schema.Features {

			if v, ok := features[f]; ok {
				full[f] = v
			} else if last, ok := a.last[groupID][f]; ok {
				full[f] = last // carry forward
			} else {
				full[f] = 0 // fallback
			}
		}

		// copy map (IMPORTANT)
		safe := make(map[string]float64)
		for k, v := range full {
			safe[k] = v
		}

		proc := a.manager.GetProcessor(groupID)

		_ = proc.Ingest(SignalInput{
			SignalID: groupID,
			GroupID:  groupID,
			Time:     float64(tick) * a.interval,
			Values:   safe,
		})
	}

	delete(a.buffer, tick)
}

/*
flush all ticks manually
*/
func (a *Aggregator) FlushAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for tick := range a.buffer {
		a.flushTick(tick)
	}
}

/*
auto flush loop
*/
func (a *Aggregator) Start() {

	ticker := time.NewTicker(time.Duration(a.interval * float64(time.Second)))

	for range ticker.C {

		a.mu.Lock()

		for tick := range a.buffer {
			a.flushTick(tick)
		}

		// simple buffer limit
		if len(a.buffer) > a.maxTicks {
			for tick := range a.buffer {
				delete(a.buffer, tick)
				break
			}
		}

		a.mu.Unlock()
	}
}