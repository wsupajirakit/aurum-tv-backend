package source

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"aurumtv/internal/channel"
	"aurumtv/internal/hls"
	"aurumtv/internal/platform/pool"
)

// Channels is the part of the channel repository the monitor needs.
type Channels interface {
	Get(id string) *channel.Channel
	List(channel.Filter) []channel.Channel
	UpdateSourceURL(channelID, sourceID, url string) (*channel.Channel, bool, error)
	SetActiveSource(channelID, sourceID, url string) (*channel.Channel, bool, error)
}

// Outcome of probing one source: the result and the url actually probed (a resolver may renew it).
type Outcome struct {
	Result      hls.Result
	ResolvedURL string
}

// ProbeFunc probes one source. Production resolves and plays it; tests inject a fake.
type ProbeFunc func(ctx context.Context, s channel.Source, forceResolve bool) Outcome

type MonitorOptions struct {
	Interval       time.Duration
	Concurrency    int
	ConfirmDelay   time.Duration
	ReportCooldown time.Duration
	Thresholds     Thresholds
	Schedule       Schedule
	Policy         Policy
}

// Switch records one source change.
type Switch struct {
	ChannelID string `json:"channelId"`
	From      string `json:"from"`
	To        string `json:"to"`
	Reason    string `json:"reason"`
}

type CycleResult struct {
	Cycle    int      `json:"cycle"`
	Probed   int      `json:"probed"`
	Switches []Switch `json:"switches"`
}

// Monitor is the self-healing loop.
//
// Every cycle:
//  1. probe each channel's active source (and backups that are due) with bounded concurrency;
//  2. if an active source fails: wait ConfirmDelay, re-probe it, and probe all of that channel's backups;
//  3. update health (with hysteresis) and let Choose decide; switch when needed.
//
// A switch changes the channel url → updatedAt moves → every TV picks up the new link.
// App reports only expedite a check; nothing switches without a real probe confirming it.
type Monitor struct {
	channels Channels
	store    *Store
	probe    ProbeFunc
	opt      MonitorOptions
	log      *slog.Logger

	// runMu serializes cycles: a channel check requested during another cycle waits its turn
	// (reports are never dropped) and always gets its own result.
	runMu sync.Mutex

	mu           sync.Mutex
	cycle        int
	fullRunning  bool
	fullDone     chan struct{}
	lastFull     CycleResult
	lastReportAt map[string]time.Time
	now          func() time.Time

	stop chan struct{}
	wg   sync.WaitGroup
}

func NewMonitor(channels Channels, store *Store, probe ProbeFunc, opt MonitorOptions, log *slog.Logger) *Monitor {
	return &Monitor{channels: channels, store: store, probe: probe, opt: opt, log: log,
		lastReportAt: map[string]time.Time{}, now: time.Now}
}

// UseProbe replaces the prober (tests).
func (m *Monitor) UseProbe(p ProbeFunc) { m.probe = p }

type target struct {
	ch           channel.Channel
	src          channel.Source
	active       bool
	forceResolve bool
}

func (m *Monitor) probeOne(ctx context.Context, cycle int, t target) hls.Result {
	out := m.probe(ctx, t.src, t.forceResolve)
	if out.ResolvedURL != "" && out.ResolvedURL != t.src.URL {
		if _, _, err := m.channels.UpdateSourceURL(t.ch.ID, t.src.ID, out.ResolvedURL); err != nil {
			m.log.Error("saving a renewed source url failed", "channel", t.ch.Name, "err", err)
		}
	}
	m.store.Set(t.ch.ID, t.src.ID, Apply(m.store.Get(t.ch.ID, t.src.ID), out.Result, cycle, m.opt.Thresholds))
	return out.Result
}

// reconcile applies the selection decision to one channel.
func (m *Monitor) reconcile(channelID string) *Switch {
	ch := m.channels.Get(channelID)
	if ch == nil || len(ch.Sources) == 0 {
		return nil
	}
	d := Choose(ch.Sources, func(id string) Health { return m.store.Get(ch.ID, id) }, ch.ActiveID(), m.opt.Policy)
	src := ch.Source(d.ActiveID)
	if src == nil || src.URL == "" || (d.ActiveID == ch.ActiveID() && src.URL == ch.URL) {
		return nil
	}
	if _, _, err := m.channels.SetActiveSource(ch.ID, d.ActiveID, src.URL); err != nil {
		m.log.Error("switching source failed", "channel", ch.Name, "err", err)
		return nil
	}
	m.log.Warn("source switched", "channel", ch.Name, "from", ch.ActiveID(), "to", d.ActiveID, "reason", d.Reason)
	return &Switch{ChannelID: ch.ID, From: ch.ActiveID(), To: d.ActiveID, Reason: d.Reason}
}

func (m *Monitor) runCycle(ctx context.Context, channelID string, force bool) CycleResult {
	m.mu.Lock()
	m.cycle++
	cycle := m.cycle
	m.mu.Unlock()

	var list []channel.Channel
	if channelID != "" {
		if c := m.channels.Get(channelID); c != nil {
			list = []channel.Channel{*c}
		}
	} else {
		list = m.channels.List(channel.Filter{Enabled: ptr(true)})
	}

	var due []target
	for _, ch := range list {
		for _, src := range ch.Sources {
			active := src.ID == ch.ActiveID()
			if force || Due(m.store.Get(ch.ID, src.ID), cycle, active, m.opt.Schedule) {
				due = append(due, target{ch: ch, src: src, active: active})
			}
		}
	}
	results := pool.Run(due, m.opt.Concurrency, func(t target) hls.Result { return m.probeOne(ctx, cycle, t) })

	var followUp []target
	for i, t := range due {
		if !t.active || results[i].OK {
			continue
		}
		followUp = append(followUp, target{ch: t.ch, src: t.src, forceResolve: true})
		for _, backup := range t.ch.Sources {
			if backup.ID != t.src.ID && !probedIn(due, t.ch.ID, backup.ID) {
				followUp = append(followUp, target{ch: t.ch, src: backup})
			}
		}
	}
	if len(followUp) > 0 {
		select {
		case <-time.After(m.opt.ConfirmDelay):
		case <-ctx.Done():
			return CycleResult{Cycle: cycle, Probed: len(due)}
		}
		pool.Run(followUp, m.opt.Concurrency, func(t target) hls.Result { return m.probeOne(ctx, cycle, t) })
	}

	res := CycleResult{Cycle: cycle, Probed: len(due), Switches: []Switch{}}
	for _, ch := range list {
		if sw := m.reconcile(ch.ID); sw != nil {
			res.Switches = append(res.Switches, *sw)
		}
	}
	if channelID == "" {
		valid := map[string]bool{}
		for _, ch := range m.channels.List(channel.Filter{}) {
			for _, s := range ch.Sources {
				valid[Key(ch.ID, s.ID)] = true
			}
		}
		m.store.Prune(valid)
	}
	return res
}

func probedIn(list []target, channelID, sourceID string) bool {
	for _, t := range list {
		if t.ch.ID == channelID && t.src.ID == sourceID {
			return true
		}
	}
	return false
}

// RunCycle runs one exclusive cycle.
//   - full cycle (channelID == "") while another full cycle runs → shares that run's result;
//   - channel cycle → waits for any running cycle, then runs (so a report is never dropped).
func (m *Monitor) RunCycle(ctx context.Context, channelID string, force bool) CycleResult {
	if channelID != "" {
		m.runMu.Lock()
		defer m.runMu.Unlock()
		return m.runCycle(ctx, channelID, force)
	}
	m.mu.Lock()
	if m.fullRunning {
		wait := m.fullDone
		m.mu.Unlock()
		<-wait
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.lastFull
	}
	m.fullRunning, m.fullDone = true, make(chan struct{})
	m.mu.Unlock()

	m.runMu.Lock()
	res := m.runCycle(ctx, "", force)
	m.runMu.Unlock()

	m.mu.Lock()
	m.fullRunning, m.lastFull = false, res
	close(m.fullDone)
	m.mu.Unlock()
	return res
}

// Report: the app could not play channelID → expedite a real check (with a per-channel cooldown).
// It returns true when accepted. The check runs in the background.
func (m *Monitor) Report(channelID string) bool {
	m.mu.Lock()
	now := m.now()
	if now.Sub(m.lastReportAt[channelID]) < m.opt.ReportCooldown {
		m.mu.Unlock()
		return false
	}
	m.lastReportAt[channelID] = now
	m.mu.Unlock()
	go m.RunCycle(context.Background(), channelID, true)
	return true
}

// SourceView is a source with its health (API/admin).
type SourceView struct {
	channel.Source
	Active bool   `json:"active"`
	Health Health `json:"health"`
}

func (m *Monitor) Describe(ch channel.Channel) []SourceView {
	out := make([]SourceView, len(ch.Sources))
	for i, s := range ch.Sources {
		out[i] = SourceView{Source: s, Active: s.ID == ch.ActiveID(), Health: m.store.Get(ch.ID, s.ID)}
	}
	return out
}

// Start runs a cycle now and then every Interval until Stop.
func (m *Monitor) Start() {
	m.stop = make(chan struct{})
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { <-m.stop; cancel() }()
		ticker := time.NewTicker(m.opt.Interval)
		defer ticker.Stop()
		for {
			m.RunCycle(ctx, "", false)
			select {
			case <-ticker.C:
			case <-m.stop:
				return
			}
		}
	}()
}

func (m *Monitor) Stop() {
	if m.stop != nil {
		close(m.stop)
		m.wg.Wait()
		m.stop = nil
	}
}

func ptr[T any](v T) *T { return &v }
