package phase1_signal

import (
	"sort"
	"sync"
	"time"
)

type managedProcessor struct {
	p          *Processor
	lastAccess time.Time
}

type Manager struct {
	mu sync.RWMutex // read-heavy; RWMutex preferred over Mutex

	processors map[string]*managedProcessor

	schema     *SignalSchema
	interval   float64
	windowSize int
	alpha      float64

	maxProcessors int
	ttl           time.Duration
}

// NewManager creates a Manager with the given signal schema, scrape interval,
// rolling-window size, and EMA smoothing factor.
func NewManager(schema *SignalSchema, interval float64, window int, alpha float64) *Manager {
	m := &Manager{
		processors:    make(map[string]*managedProcessor),
		schema:        schema,
		interval:      interval,
		windowSize:    window,
		alpha:         alpha,
		maxProcessors: 1000,
		ttl:           5 * time.Minute,
	}
	go m.cleanupLoop()
	return m
}

// GetProcessor returns the Processor for the given signal ID, creating one if
// it does not yet exist. Safe for concurrent use.
func (m *Manager) GetProcessor(id string) *Processor {
	// Fast read path.
	m.mu.RLock()
	mp, ok := m.processors[id]
	m.mu.RUnlock()

	if ok {
		m.mu.Lock()
		mp.lastAccess = time.Now()
		m.mu.Unlock()
		return mp.p
	}

	// Slow path: create new processor.
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-checked locking: another goroutine may have inserted while we
	// were waiting for the write lock.
	if mp, ok := m.processors[id]; ok {
		mp.lastAccess = time.Now()
		return mp.p
	}

	if len(m.processors) >= m.maxProcessors {
		m.evictOne()
	}

	p := NewProcessor(m.schema, m.interval, m.windowSize, m.alpha, id)
	m.processors[id] = &managedProcessor{p: p, lastAccess: time.Now()}
	return p
}

// evictOne removes the least-recently-accessed processor. Caller must hold write lock.
func (m *Manager) evictOne() {
	var oldestKey string
	var oldestTime time.Time
	for k, v := range m.processors {
		if oldestKey == "" || v.lastAccess.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.lastAccess
		}
	}
	if oldestKey != "" {
		delete(m.processors, oldestKey)
	}
}

// cleanupLoop periodically removes processors idle longer than ttl.
func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		now := time.Now()
		m.mu.Lock()
		for k, v := range m.processors {
			if now.Sub(v.lastAccess) > m.ttl {
				delete(m.processors, k)
			}
		}
		m.mu.Unlock()
	}
}

// GetSignalIDs returns a sorted, stable list of all currently-active signal IDs.
// Used by the realdata adapter to enumerate processors and build a Dataset.
func (m *Manager) GetSignalIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.processors))
	for k := range m.processors {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	return ids
}

// Stats returns basic observability counters.
func (m *Manager) Stats() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]int{"processors": len(m.processors)}
}

// ListIDs returns the IDs of all active processors in sorted order.
// Called by realtime.PollerBridge to extract NodeStates after each poll.
func (m *Manager) ListIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.processors))
	for id := range m.processors {
		ids = append(ids, id)
	}
	return ids
}
