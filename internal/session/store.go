// Package session implements TV device sessions: a short-lived access token plus a rotating
// refresh token. Only HMAC hashes of refresh tokens are stored, in DATA_DIR/sessions.json, so a
// leaked file cannot be replayed and sessions survive restarts and redeploys.
package session

import (
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"aurumtv/internal/platform/fsx"
)

// Record is one device session. JSON names match the stored file format.
type Record struct {
	ID             string  `json:"id"`
	DeviceID       string  `json:"deviceId"`
	Platform       *string `json:"platform"`
	CreatedAt      int64   `json:"createdAt"`
	CurrentHash    string  `json:"currentHash"`
	PrevHash       *string `json:"prevHash"`
	PrevValidUntil int64   `json:"prevValidUntil"`
	RotatedAt      int64   `json:"rotatedAt"`
	ExpiresAt      int64   `json:"expiresAt"`
}

type fileFormat struct {
	Sessions []Record `json:"sessions"`
}

const persistDebounce = 500 * time.Millisecond

// Store keeps sessions in memory, indexed by id and by refresh-token hash (current and previous).
type Store struct {
	mu      sync.Mutex
	file    string
	byID    map[string]*Record
	byHash  map[string]string
	pending *time.Timer
	writeMu sync.Mutex
	log     *slog.Logger
}

func NewStore(file string, log *slog.Logger) *Store {
	return &Store{file: file, byID: map[string]*Record{}, byHash: map[string]string{}, log: log}
}

// Load reads the file, dropping sessions that already expired. A missing file starts empty.
func (s *Store) Load(nowUnix int64) error {
	var data fileFormat
	if err := fsx.ReadJSON(s.file, &data); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		s.log.Warn("sessions file unreadable — starting empty (devices will open new sessions)", "err", err)
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range data.Sessions {
		rec := data.Sessions[i]
		if rec.ExpiresAt > nowUnix {
			s.index(&rec)
		}
	}
	return nil
}

func (s *Store) index(r *Record) {
	s.byID[r.ID] = r
	s.byHash[r.CurrentHash] = r.ID
	if r.PrevHash != nil && *r.PrevHash != "" {
		s.byHash[*r.PrevHash] = r.ID
	}
}

func (s *Store) unindex(r *Record) {
	delete(s.byHash, r.CurrentHash)
	if r.PrevHash != nil {
		delete(s.byHash, *r.PrevHash)
	}
	delete(s.byID, r.ID)
}

func (s *Store) FindByHash(hash string) *Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byHash[hash]; ok {
		if r, ok := s.byID[id]; ok {
			cp := *r
			return &cp
		}
	}
	return nil
}

func (s *Store) FindByDevice(deviceID string) *Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.byID {
		if r.DeviceID == deviceID {
			cp := *r
			return &cp
		}
	}
	return nil
}

// Active reports whether a session still exists and has not expired — access tokens of revoked
// sessions stop working immediately instead of at expiry.
func (s *Store) Active(id string, nowUnix int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	return ok && r.ExpiresAt > nowUnix
}

func (s *Store) Save(r Record) {
	s.mu.Lock()
	if old, ok := s.byID[r.ID]; ok {
		s.unindex(old)
	}
	s.index(&r)
	s.mu.Unlock()
	s.schedulePersist()
}

func (s *Store) Remove(id string) {
	s.mu.Lock()
	if r, ok := s.byID[id]; ok {
		s.unindex(r)
	}
	s.mu.Unlock()
	s.schedulePersist()
}

// Sweep drops expired sessions.
func (s *Store) Sweep(nowUnix int64) {
	s.mu.Lock()
	changed := false
	for _, r := range s.byID {
		if r.ExpiresAt <= nowUnix {
			s.unindex(r)
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.schedulePersist()
	}
}

// schedulePersist coalesces bursts of changes into one write.
func (s *Store) schedulePersist() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil {
		return
	}
	s.pending = time.AfterFunc(persistDebounce, func() {
		s.mu.Lock()
		s.pending = nil
		s.mu.Unlock()
		if err := s.write(); err != nil {
			s.log.Error("writing sessions failed", "err", err)
		}
	})
}

func (s *Store) snapshot() fileFormat {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := fileFormat{Sessions: make([]Record, 0, len(s.byID))}
	for _, r := range s.byID {
		out.Sessions = append(out.Sessions, *r)
	}
	return out
}

func (s *Store) write() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return fsx.WriteJSON(s.file, s.snapshot(), fsx.WriteOptions{})
}

// Flush writes pending changes now (called on shutdown).
func (s *Store) Flush() error {
	s.mu.Lock()
	pending := s.pending != nil
	if pending {
		s.pending.Stop()
		s.pending = nil
	}
	s.mu.Unlock()
	if !pending {
		return nil
	}
	return s.write()
}
