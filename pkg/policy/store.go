// Package policy provides persistent storage for trained RL policy weights.
// Weights are serialised as JSON files under a configurable directory, keyed
// by target node ID. On the next pipeline run for the same target, weights
// are loaded and used to warmstart training instead of re-initialising from
// random — giving the policy a meaningful head-start from prior observations.
package policy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Weights is the serialisable representation of a trained Policy.
// It mirrors phase5_insight.Policy exactly so we can round-trip without
// importing that package (which would create an import cycle).
type Weights struct {
	W [][]float64 `json:"w"`
	B []float64   `json:"b"`
}

// Store persists and retrieves policy weights for each target node.
// Thread-safe: concurrent reads and writes to the same target are safe.
type Store struct {
	mu      sync.RWMutex
	baseDir string
	log     *slog.Logger
}

// New creates a Store backed by the given directory.
// The directory is created (with parents) if it does not exist.
func New(dir string, log *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("policy store: cannot create dir %q: %w", dir, err)
	}
	if log == nil {
		log = slog.Default()
	}
	return &Store{baseDir: dir, log: log}, nil
}

// Load reads the persisted weights for targetID.
// Returns (nil, nil) when no weights have been saved yet — callers should
// treat nil as "no prior; use random init".
func (s *Store) Load(targetID string) (*Weights, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.path(targetID)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil // first run for this target
	}
	if err != nil {
		return nil, fmt.Errorf("policy store: read %q: %w", path, err)
	}

	var w Weights
	if err := json.Unmarshal(data, &w); err != nil {
		// Corrupt file: log and treat as missing so pipeline continues.
		s.log.Warn("policy store: corrupt weights file — discarding",
			slog.String("target", targetID),
			slog.String("path", path),
			slog.Any("error", err),
		)
		return nil, nil
	}
	s.log.Debug("policy store: loaded weights",
		slog.String("target", targetID),
		slog.Int("actions", len(w.W)),
	)
	return &w, nil
}

// Save persists weights for targetID atomically (write to tmp then rename).
func (s *Store) Save(targetID string, w *Weights) error {
	if w == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("policy store: marshal: %w", err)
	}

	// Atomic write: write to a temp file in the same directory, then rename.
	tmp := s.path(targetID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return fmt.Errorf("policy store: write tmp %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path(targetID)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("policy store: rename %q: %w", tmp, err)
	}

	s.log.Debug("policy store: saved weights",
		slog.String("target", targetID),
		slog.Int("actions", len(w.W)),
	)
	return nil
}

// path returns the full file path for the given targetID.
// Characters unsafe in filenames are replaced with underscores.
func (s *Store) path(targetID string) string {
	safe := make([]byte, len(targetID))
	for i, c := range []byte(targetID) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9', c == '-', c == '.':
			safe[i] = c
		default:
			safe[i] = '_'
		}
	}
	return filepath.Join(s.baseDir, string(safe)+".json")
}
