// Package realdata bridges the phase1 signal-processing layer to data.Dataset.
//
// Deprecated: MetricsStore was an alternative metrics accumulator that was
// never wired into the production pipeline. The canonical store used by all
// production code is pkg/metricsstore.Store. This file is retained only to
// avoid breaking any external consumers that may reference the type; it will
// be removed in a future version.
//
// All new code should use pkg/metricsstore instead.
package realdata
