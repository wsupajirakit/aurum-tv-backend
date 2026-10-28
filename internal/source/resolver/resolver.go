// Package resolver turns a source into a playable url. Add a kind: implement Resolver and register it in New.
package resolver

import (
	"context"
	"net/http"
	"time"

	"aurumtv/internal/channel"
	"aurumtv/internal/platform/clock"
	"aurumtv/internal/stream"
)

type Resolver interface {
	// Resolve returns the url to use now ("" when none). force skips the freshness check.
	Resolve(ctx context.Context, s channel.Source, force bool) string
	// NeedsRefresh reports whether the stored url should be renewed.
	NeedsRefresh(s channel.Source) bool
}

// Registry maps resolver kinds to implementations; unknown kinds fall back to static.
type Registry map[string]Resolver

func (r Registry) For(s channel.Source) Resolver {
	if res, ok := r[s.Resolver]; ok {
		return res
	}
	return r[channel.ResolverStatic]
}

type Static struct{}

func (Static) Resolve(_ context.Context, s channel.Source, _ bool) string { return s.URL }
func (Static) NeedsRefresh(channel.Source) bool                           { return false }

// Ch3Plus resolves a signed link from the source page and renews it before it expires.
// When the page cannot be reached the stored url is kept (it may still play).
type Ch3Plus struct {
	Client      *http.Client
	SourceURL   string
	Timeout     time.Duration
	RenewBefore time.Duration
	Clock       clock.Clock
}

func (c Ch3Plus) NeedsRefresh(s channel.Source) bool {
	return s.URL == "" || stream.Expiring(s.URL, c.RenewBefore, c.Clock.Now())
}

func (c Ch3Plus) Resolve(ctx context.Context, s channel.Source, force bool) string {
	if !force && !c.NeedsRefresh(s) {
		return s.URL
	}
	if fresh, err := stream.FreshURL(ctx, c.Client, c.SourceURL, c.Timeout); err == nil {
		return fresh
	}
	return s.URL
}

func New(ch3 Ch3Plus) Registry {
	return Registry{channel.ResolverStatic: Static{}, channel.ResolverCh3Plus: ch3}
}
