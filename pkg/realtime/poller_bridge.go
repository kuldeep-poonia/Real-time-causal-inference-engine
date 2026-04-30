
package realtime

import (
	"context"
	"log"
	"sort"
	"time"

	"absia/internal/intelligence/phase1_signal"
	"absia/pkg/metricsstore"
)

const (
	// defaultPollStep is how often the poller fetches from Prometheus.
	defaultPollStep = 15 * time.Second

	// defaultWindow is the number of signal ticks the Processor keeps in memory.
	defaultWindow = 60

	// defaultAlpha is the EMA smoothing factor for the Processor.
	defaultAlpha = 0.3

	// extractInterval controls how often NodeStates are extracted from
	// processors and written into the metricsstore.
	extractInterval = 15 * time.Second
)

// PollerBridge owns a phase1_signal Manager and a Poller, drives both, and
// copies NodeStates into the metricsstore on every extract tick.
type PollerBridge struct {
	prometheusURL string
	store         *metricsstore.Store
	manager       *phase1_signal.Manager
}

// NewPollerBridge creates a bridge pointing at the given Prometheus base URL.
// The bridge is inactive until Start is called.
func NewPollerBridge(prometheusURL string, store *metricsstore.Store) *PollerBridge {
	// Schema: single "value" feature per metric series.
	// The Processor converts the raw value stream into arrival rate (λ)
	// and service rate proxy (μ) via its M/M/1 physics model.
	schema := phase1_signal.NewSignalSchema([]string{"value"})
	mgr := phase1_signal.NewManager(schema, 1.0, defaultWindow, defaultAlpha)

	return &PollerBridge{
		prometheusURL: prometheusURL,
		store:         store,
		manager:       mgr,
	}
}

// Start launches both the Prometheus poller goroutine and the extraction loop.
// Blocks until ctx is cancelled.
func (b *PollerBridge) Start(ctx context.Context) {
	// Query for request rate and error rate — two of the most common
	// signals for distributed system root-cause analysis.
	// These PromQL queries group results by "job" label, which maps to
	// service names in standard Kubernetes deployments.
	queries := []string{
    `sum(rate(container_cpu_usage_seconds_total[1m])) by (name)`,
    `sum(container_memory_usage_bytes) by (name)`,
}

	// Launch one poller per query.
	for _, q := range queries {
		poller := phase1_signal.NewPoller(b.prometheusURL, q, defaultPollStep, b.manager)
		go poller.Start(ctx)
		log.Printf("[PollerBridge] started poller for query: %s", q)
	}

	// Extraction loop: on every tick, read NodeStates from all known
	// processors and write them into the metrics store.
	ticker := time.NewTicker(extractInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[PollerBridge] stopping extraction loop")
			return
		case <-ticker.C:
			b.extractAndStore()
		}
	}
}

// extractAndStore reads the current NodeState from every known processor
// in the Manager and writes it as a NodeSample into the metrics store.
// The Manager's ListIDs method (added in manager.go) provides the list
// of known group IDs accumulated since the last Prometheus poll.
func (b *PollerBridge) extractAndStore() {
	ids := b.manager.ListIDs()
	if len(ids) == 0 {
		return
	}
	sort.Strings(ids) // deterministic ordering for reproducible graph discovery

	now := time.Now()
	ts := float64(now.Unix())

	for _, id := range ids {
		proc := b.manager.GetProcessor(id)
		ns := proc.GetNodeState()

		// Only store samples where the processor has observed enough data
		// to produce meaningful arrival-rate and service-rate estimates.
		if ns.ArrivalRate <= 0 && ns.Load <= 0 {
			continue
		}

		b.store.Put(id, metricsstore.NodeSample{
			ArrivalRate: ns.ArrivalRate,
			ServiceRate: ns.ServiceRate,
			QueueLength: ns.QueueLength,
			Timestamp:   ts,
			WallTime:    now,
		})

		log.Printf("[PollerBridge] node=%s λ=%.4f μ=%.4f ρ=%.4f L=%.4f",
			id, ns.ArrivalRate, ns.ServiceRate, ns.Load, ns.QueueLength)
	}
}
