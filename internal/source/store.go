package source

import (
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"aurumtv/internal/platform/fsx"
)

// Store keeps source health in memory and in DATA_DIR/source-health.json. It is deliberately
// separate from channels.json: routine probes must never move the channel list's updatedAt
// (apps reload the list when it changes).
type Store struct {
	mu      sync.Mutex
	file    string
	entries map[string]Health
	pending *time.Timer
	writeMu sync.Mutex
	log     *slog.Logger
}

const healthPersistDebounce = 2 * time.Second

func NewStore(file string, log *slog.Logger) *Store {
	return &Store{file: file, entries: map[string]Health{}, log: log}
}

func Key(channelID, sourceID string) string { return channelID + "/" + sourceID }

type healthFile struct {
	Entries map[string]Health `json:"entries"`
}

func (s *Store) Load() error {
	var f healthFile
	if err := fsx.ReadJSON(s.file, &f); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.log.Warn("source-health.json unreadable — starting fresh", "err", err)
		}
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, h := range f.Entries {
		h.LastProbeCycle = nil
		if h.Status == "" {
			h.Status = StatusUnknown
		}
		s.entries[k] = h
	}
	return nil
}

func (s *Store) Get(channelID, sourceID string) Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.entries[Key(channelID, sourceID)]; ok {
		return h
	}
	return EmptyHealth()
}

func (s *Store) Set(channelID, sourceID string, h Health) {
	s.mu.Lock()
	s.entries[Key(channelID, sourceID)] = h
	s.mu.Unlock()
	s.schedule()
}

// Prune drops health of sources that no longer exist.
func (s *Store) Prune(valid map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.entries {
		if !valid[k] {
			delete(s.entries, k)
		}
	}
}

func (s *Store) schedule() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil {
		return
	}
	s.pending = time.AfterFunc(healthPersistDebounce, func() {
		s.mu.Lock()
		s.pending = nil
		s.mu.Unlock()
		if err := s.write(); err != nil {
			s.log.Error("writing source health failed", "err", err)
		}
	})
}

func (s *Store) write() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	out := healthFile{Entries: make(map[string]Health, len(s.entries))}
	for k, h := range s.entries {
		h.LastProbeCycle = nil
		out.Entries[k] = h
	}
	s.mu.Unlock()
	return fsx.WriteJSON(s.file, out, fsx.WriteOptions{})
}

// Flush writes now (called on shutdown).
func (s *Store) Flush() error {
	s.mu.Lock()
	if s.pending != nil {
		s.pending.Stop()
		s.pending = nil
	}
	s.mu.Unlock()
	return s.write()
}
