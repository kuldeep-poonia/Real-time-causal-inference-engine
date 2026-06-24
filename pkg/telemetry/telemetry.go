package telemetry

// Tracker defines the enterprise interface for internal engine telemetry.
type Tracker interface {
	// Increment adds 1 to the given counter metric.
	Increment(metric string)
	
	// Add adds value to the given counter metric.
	Add(metric string, value int64)
	
	// RecordTime records an execution duration in milliseconds for a metric.
	RecordTime(metric string, durationMS float64)
}

// globalTracker is the currently configured telemetry backend.
var globalTracker Tracker

// SetTracker configures the active telemetry tracker.
func SetTracker(t Tracker) {
	globalTracker = t
}

// Get returns the active telemetry tracker, or a no-op implementation if none is set.
func Get() Tracker {
	if globalTracker == nil {
		return &noopTracker{}
	}
	return globalTracker
}

type noopTracker struct{}
func (n *noopTracker) Increment(metric string) {}
func (n *noopTracker) Add(metric string, value int64) {}
func (n *noopTracker) RecordTime(metric string, durationMS float64) {}
