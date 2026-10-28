package source

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"aurumtv/internal/channel"
	"aurumtv/internal/source/resolver"
)

// Refresher renews the urls of resolver-type sources (e.g. signed links) — at startup, on a timer,
// and on demand. Concurrent requests share one run.
type Refresher struct {
	channels Channels
	registry resolver.Registry
	log      *slog.Logger

	mu       sync.Mutex
	inFlight chan struct{}
}

func NewRefresher(channels Channels, registry resolver.Registry, log *slog.Logger) *Refresher {
	return &Refresher{channels: channels, registry: registry, log: log}
}

func (r *Refresher) refreshSource(ctx context.Context, ch channel.Channel, s channel.Source, force bool) {
	res := r.registry.For(s)
	if !force && !res.NeedsRefresh(s) {
		return
	}
	fresh := res.Resolve(ctx, s, true)
	if fresh == "" || fresh == s.URL {
		return
	}
	if _, changed, err := r.channels.UpdateSourceURL(ch.ID, s.ID, fresh); err != nil {
		r.log.Error("saving renewed url failed", "channel", ch.Name, "err", err)
	} else if changed {
		r.log.Info("source url renewed", "channel", ch.Name, "source", s.ID)
	}
}

// EnsureFresh renews every resolver source that is about to expire.
func (r *Refresher) EnsureFresh(ctx context.Context) {
	r.mu.Lock()
	if r.inFlight != nil {
		wait := r.inFlight
		r.mu.Unlock()
		<-wait
		return
	}
	r.inFlight = make(chan struct{})
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		close(r.inFlight)
		r.inFlight = nil
		r.mu.Unlock()
	}()
	for _, ch := range r.channels.List(channel.Filter{}) {
		for _, s := range ch.Sources {
			if s.Resolver != channel.ResolverStatic {
				r.refreshSource(ctx, ch, s, false)
			}
		}
	}
}

// ForceRefresh renews the resolver sources of one channel now and returns the channel.
func (r *Refresher) ForceRefresh(ctx context.Context, id string) *channel.Channel {
	ch := r.channels.Get(id)
	if ch == nil {
		return nil
	}
	for _, s := range ch.Sources {
		if s.Resolver != channel.ResolverStatic {
			r.refreshSource(ctx, *ch, s, true)
		}
	}
	return r.channels.Get(id)
}

// Run renews on a timer until ctx is done.
func (r *Refresher) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			r.EnsureFresh(ctx)
		case <-ctx.Done():
			return
		}
	}
}
