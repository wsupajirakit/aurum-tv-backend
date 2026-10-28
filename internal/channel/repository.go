package channel

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"

	"aurumtv/internal/config"
	"aurumtv/internal/platform/clock"
	"aurumtv/internal/platform/fsx"
)

// Event is broadcast to live listeners (SSE) after every change.
type Event struct {
	Type    string   `json:"type"`
	Channel *Channel `json:"channel,omitempty"`
	At      string   `json:"at"`
}

// Meta summarizes the list (used by /api/health and the list response).
type Meta struct {
	Count     int     `json:"count"`
	UpdatedAt *string `json:"updatedAt"`
	Version   int     `json:"version"`
}

// Filter for List; Enabled nil means "any".
type Filter struct {
	Query   string
	Group   string
	Enabled *bool
}

// Repository is the single writer of channel data. Every change runs under one lock:
// mutate → persist atomically → notify, so concurrent requests never overwrite each other.
type Repository struct {
	mu        sync.RWMutex
	state     State
	paths     config.Paths
	legacyIDs []string
	clock     clock.Clock
	log       *slog.Logger

	subMu sync.Mutex
	subs  map[chan Event]struct{}
}

func NewRepository(paths config.Paths, legacyIDs []string, c clock.Clock, log *slog.Logger) *Repository {
	return &Repository{paths: paths, legacyIDs: legacyIDs, clock: c, log: log, subs: map[chan Event]struct{}{}}
}

// Load restores data: main file → backup file → embedded seed, then migrates old schemas.
func (r *Repository) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	raw, source, err := r.readInitial()
	if err != nil {
		return err
	}
	version, channels, migrated := Migrate(int(raw.Version), raw.Channels, r.legacyIDs)
	now := r.clock.Now()
	list := make([]Channel, len(channels))
	for i, c := range channels {
		list[i] = Normalize(c, i, now)
	}
	sort.SliceStable(list, func(a, b int) bool { return list[a].Order < list[b].Order })
	r.state = State{Version: version, UpdatedAt: raw.UpdatedAt, Channels: list}

	if source != "main" || len(migrated) > 0 {
		if err := r.persistLocked(); err != nil {
			return err
		}
	}
	if len(migrated) > 0 {
		r.log.Info("channel data migrated (previous file kept as channels.bak.json)", "steps", strings.Join(migrated, ", "))
	}
	r.log.Info("channels loaded", "count", len(list), "from", source)
	return nil
}

func (r *Repository) readInitial() (rawState, string, error) {
	var s rawState
	err := fsx.ReadJSON(r.paths.ChannelsFile, &s)
	if err == nil && s.Channels != nil {
		return s, "main", nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		r.log.Warn("channels.json unreadable — trying the backup", "err", err)
	}
	var b rawState
	if berr := fsx.ReadJSON(r.paths.BackupFile, &b); berr == nil && b.Channels != nil {
		r.log.Warn("restored channels from the backup file")
		return b, "backup", nil
	}
	seed, serr := Seed()
	if serr != nil {
		return rawState{}, "", fmt.Errorf("channel: seed: %w", serr)
	}
	return rawState{Version: SchemaVersion, Channels: seed}, "seed", nil
}

func (r *Repository) persistLocked() error {
	stamp := ISO(r.clock.Now())
	r.state.UpdatedAt = &stamp
	return fsx.WriteJSON(r.paths.ChannelsFile, r.state, fsx.WriteOptions{
		TmpFile: r.paths.TmpFile, BackupFile: r.paths.BackupFile, Pretty: true,
	})
}

// mutate runs fn under the write lock, persists, then notifies listeners.
func (r *Repository) mutate(eventType string, fn func() (*Channel, bool)) (*Channel, error) {
	r.mu.Lock()
	ch, changed := fn()
	if !changed {
		r.mu.Unlock()
		return ch, nil
	}
	if err := r.persistLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	event := Event{Type: eventType, Channel: ch, At: *r.state.UpdatedAt}
	r.mu.Unlock()
	r.notify(event)
	return ch, nil
}

// ---------- reads ----------

func (r *Repository) List(f Filter) []Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	q := strings.ToLower(f.Query)
	out := make([]Channel, 0, len(r.state.Channels))
	for _, c := range r.state.Channels {
		if q != "" && !strings.Contains(strings.ToLower(c.Name), q) && !strings.Contains(strings.ToLower(c.Group), q) {
			continue
		}
		if f.Group != "" && c.Group != f.Group {
			continue
		}
		if f.Enabled != nil && c.Enabled != *f.Enabled {
			continue
		}
		out = append(out, clone(c))
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Order != out[b].Order {
			return out[a].Order < out[b].Order
		}
		return out[a].Name < out[b].Name
	})
	return out
}

func (r *Repository) Get(id string) *Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if i := r.indexLocked(id); i != -1 {
		c := clone(r.state.Channels[i])
		return &c
	}
	return nil
}

func (r *Repository) Groups() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := map[string]bool{}
	for _, c := range r.state.Channels {
		if c.Group != "" {
			set[c.Group] = true
		}
	}
	out := make([]string, 0, len(set))
	for g := range set {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

func (r *Repository) Meta() Meta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Meta{Count: len(r.state.Channels), UpdatedAt: r.state.UpdatedAt, Version: r.state.Version}
}

// ---------- writes ----------

func (r *Repository) maxOrderLocked() int {
	m := -1
	for _, c := range r.state.Channels {
		m = max(m, c.Order)
	}
	return m
}

func (r *Repository) Create(input Raw) (*Channel, error) {
	return r.mutate("created", func() (*Channel, bool) {
		if input.Order == nil {
			input.Order = ptr(float64(r.maxOrderLocked() + 1))
		}
		input.ID, input.CreatedAt, input.UpdatedAt = "", "", ""
		c := Normalize(input, len(r.state.Channels), r.clock.Now())
		r.state.Channels = append(r.state.Channels, c)
		cp := clone(c)
		return &cp, true
	})
}

func (r *Repository) Update(id string, p Patch) (*Channel, error) {
	return r.mutate("updated", func() (*Channel, bool) {
		i := r.indexLocked(id)
		if i == -1 {
			return nil, false
		}
		current := r.state.Channels[i]
		merged := Merge(current, p)
		merged.ID, merged.CreatedAt, merged.UpdatedAt = id, current.CreatedAt, ISO(r.clock.Now())
		r.state.Channels[i] = Normalize(merged, i, r.clock.Now())
		cp := clone(r.state.Channels[i])
		return &cp, true
	})
}

// UpdateSourceURL changes one source's url (a resolver renewed it, or the app synced it).
// When it is the active source the channel url follows. No change → no write, so updatedAt does
// not move and clients do not reload.
func (r *Repository) UpdateSourceURL(channelID, sourceID, url string) (*Channel, bool, error) {
	current := r.Get(channelID)
	if current == nil {
		return nil, false, nil
	}
	src := current.Source(sourceID)
	if src == nil || src.URL == url {
		return current, false, nil
	}
	sources := rawSources(current.Sources)
	for i := range sources {
		if sources[i].ID == sourceID {
			sources[i].URL = url
		}
	}
	chURL := current.URL
	if current.ActiveID() == sourceID {
		chURL = url
	}
	ch, err := r.Update(channelID, Patch{Sources: &sources, URL: &chURL})
	return ch, ch != nil, err
}

// SetActiveSource switches the source in use (decided by the self-healing monitor).
func (r *Repository) SetActiveSource(channelID, sourceID, url string) (*Channel, bool, error) {
	current := r.Get(channelID)
	if current == nil || (current.ActiveID() == sourceID && current.URL == url) {
		return current, false, nil
	}
	sources := rawSources(current.Sources)
	ch, err := r.Update(channelID, Patch{ActiveSourceID: &sourceID, URL: &url, Sources: &sources})
	return ch, ch != nil, err
}

func (r *Repository) Remove(id string) (*Channel, error) {
	return r.mutate("deleted", func() (*Channel, bool) {
		i := r.indexLocked(id)
		if i == -1 {
			return nil, false
		}
		removed := r.state.Channels[i]
		r.state.Channels = append(r.state.Channels[:i], r.state.Channels[i+1:]...)
		return &removed, true
	})
}

// BulkCreate adds several channels; items without a name or without any url/source are skipped.
func (r *Repository) BulkCreate(items []Raw) ([]Channel, error) {
	var created []Channel
	_, err := r.mutate("bulk", func() (*Channel, bool) {
		order := r.maxOrderLocked()
		now := r.clock.Now()
		for _, item := range items {
			if strings.TrimSpace(item.Name) == "" || (strings.TrimSpace(item.URL) == "" && len(item.Sources) == 0) {
				continue
			}
			if item.Order == nil {
				order++
				item.Order = ptr(float64(order))
			}
			item.ID, item.CreatedAt, item.UpdatedAt = "", "", ""
			c := Normalize(item, len(r.state.Channels), now)
			r.state.Channels = append(r.state.Channels, c)
			created = append(created, clone(c))
		}
		return nil, true
	})
	return created, err
}

// Reorder sets the order from ids; ids not listed keep their relative order after them.
func (r *Repository) Reorder(ids []string) ([]Channel, error) {
	var result []Channel
	_, err := r.mutate("reordered", func() (*Channel, bool) {
		rank := map[string]int{}
		for i, id := range ids {
			if _, dup := rank[id]; !dup {
				rank[id] = i
			}
		}
		for i := range r.state.Channels {
			if pos, ok := rank[r.state.Channels[i].ID]; ok {
				r.state.Channels[i].Order = pos
			} else {
				r.state.Channels[i].Order = len(ids) + i
			}
		}
		sort.SliceStable(r.state.Channels, func(a, b int) bool { return r.state.Channels[a].Order < r.state.Channels[b].Order })
		for i := range r.state.Channels {
			r.state.Channels[i].Order = i
		}
		for _, c := range r.state.Channels {
			result = append(result, clone(c))
		}
		return nil, true
	})
	return result, err
}

func (r *Repository) indexLocked(id string) int {
	for i, c := range r.state.Channels {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// ---------- live events ----------

// Subscribe returns a channel of events and a cancel function. Slow listeners drop events
// instead of blocking writers.
func (r *Repository) Subscribe(buffer int) (<-chan Event, func()) {
	ch := make(chan Event, buffer)
	r.subMu.Lock()
	r.subs[ch] = struct{}{}
	r.subMu.Unlock()
	return ch, func() {
		r.subMu.Lock()
		if _, ok := r.subs[ch]; ok {
			delete(r.subs, ch)
			close(ch)
		}
		r.subMu.Unlock()
	}
}

func (r *Repository) notify(e Event) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	for ch := range r.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func clone(c Channel) Channel {
	c.Sources = append([]Source(nil), c.Sources...)
	return c
}

func rawSources(list []Source) []RawSource {
	out := make([]RawSource, len(list))
	for i, s := range list {
		out[i] = RawSource{ID: s.ID, Label: s.Label, URL: s.URL, Resolver: s.Resolver, Priority: ptr(float64(s.Priority))}
	}
	return out
}
