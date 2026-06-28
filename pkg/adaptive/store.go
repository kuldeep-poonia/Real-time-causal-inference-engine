package adaptive

import (
	"os"
	"strconv"
	"sync"
)

// AdaptiveProfileStore manages AdaptiveNodeProfiles for all nodes.
type AdaptiveProfileStore struct {
	mu       sync.RWMutex
	profiles map[string]*AdaptiveNodeProfile
	sigmaK   float64
	minObs   int64
}

// NewAdaptiveProfileStore creates a new store for tracking dynamic profiles.
func NewAdaptiveProfileStore(sigmaK float64, minObs int64) *AdaptiveProfileStore {
	return &AdaptiveProfileStore{
		profiles: make(map[string]*AdaptiveNodeProfile),
		sigmaK:   sigmaK,
		minObs:   minObs,
	}
}

// GetProfile returns the profile for a node, creating it if it doesn't exist.
func (s *AdaptiveProfileStore) GetProfile(nodeID string) *AdaptiveNodeProfile {
	s.mu.RLock()
	p, ok := s.profiles[nodeID]
	s.mu.RUnlock()
	if ok {
		return p
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Double check
	p, ok = s.profiles[nodeID]
	if ok {
		return p
	}

	p = NewAdaptiveNodeProfile(nodeID, s.sigmaK, s.minObs)
	s.profiles[nodeID] = p
	return p
}

// Global default store instance for easy access across the application
var GlobalStore *AdaptiveProfileStore

func init() {
	minObs := int64(10)
	if envVal := os.Getenv("ABSIA_WARMUP_OBSERVATIONS"); envVal != "" {
		if parsed, err := strconv.ParseInt(envVal, 10, 64); err == nil && parsed >= 0 {
			minObs = parsed
		}
	}
	GlobalStore = NewAdaptiveProfileStore(3.0, minObs)
}
