package source

import (
	"context"
	"time"

	"aurumtv/internal/channel"
	"aurumtv/internal/hls"
	"aurumtv/internal/source/resolver"
	"aurumtv/internal/stream"
)

// NewProber resolves a source's url (resolvers may renew it) and then plays it with hls.Probe.
func NewProber(registry resolver.Registry, opt hls.Options) ProbeFunc {
	return func(ctx context.Context, s channel.Source, forceResolve bool) Outcome {
		url := registry.For(s).Resolve(ctx, s, forceResolve)
		if url == "" {
			reason := "no_url"
			return Outcome{Result: hls.Result{Stage: "resolve", Reason: &reason,
				CheckedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}}
		}
		o := opt
		o.Headers = stream.BrowserHeaders
		return Outcome{Result: hls.Probe(ctx, url, o), ResolvedURL: url}
	}
}
