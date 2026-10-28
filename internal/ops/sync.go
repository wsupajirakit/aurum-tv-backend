package ops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aurumtv/internal/channel"
	"aurumtv/internal/stream"
)

// SyncResult of one sync run.
type SyncResult string

const (
	SyncUpdated  SyncResult = "updated"
	SyncUpToDate SyncResult = "up-to-date"
	SyncDryRun   SyncResult = "dry-run"
)

// SyncOptions controls SyncCh3.
type SyncOptions struct {
	SourceURL string
	Timeout   time.Duration
	DryRun    bool
	Force     bool
	Client    *http.Client
}

// SyncCh3 fetches a fresh link for the ch3plus resolver source and updates the backend.
//
// Why it exists: the source site sometimes blocks cloud IP ranges, so a hosted backend cannot renew
// the link itself. Run this from a network that can reach the site; it pushes the link with the
// admin key (PATCH), which works under HMAC_MODE=enforce. The channel is found from data (the
// source whose resolver is ch3plus), never from a name or id. Only that source is changed.
func SyncCh3(ctx context.Context, admin *Admin, o SyncOptions, log Logger) (SyncResult, error) {
	channels, err := admin.ListChannels(ctx)
	if err != nil {
		return "", err
	}
	ch, src := findResolver(channels, channel.ResolverCh3Plus)
	if ch == nil {
		return "", fmt.Errorf("no channel on the backend has a %q resolver source", channel.ResolverCh3Plus)
	}
	log.Info("server channel: %s (%s) source %s expires %s", ch.Name, ch.ID, src.ID, expiry(src.URL))

	fresh, err := stream.FreshURL(ctx, o.Client, o.SourceURL, o.Timeout)
	if err != nil {
		return "", fmt.Errorf("%w (is this network blocked by the source?)", err)
	}
	if u, perr := url.Parse(fresh); perr != nil || u.Scheme != "https" || !strings.HasSuffix(u.Path, ".m3u8") {
		return "", errors.New("the source returned a link that is not an https .m3u8")
	}
	log.Info("fresh link expires %s", expiry(fresh))

	if fresh == src.URL && !o.Force {
		log.Info("the server already has the latest link")
		return SyncUpToDate, nil
	}
	if o.DryRun {
		log.Info("--dry-run: nothing was changed")
		return SyncDryRun, nil
	}
	sources := append([]Source(nil), ch.Sources...)
	for i := range sources {
		if sources[i].ID == src.ID {
			sources[i].URL = fresh
		}
	}
	updated, err := admin.PatchSources(ctx, ch.ID, sources)
	if err != nil {
		return "", err
	}
	if s := sourceByID(updated.Sources, src.ID); s == nil || s.URL != fresh {
		return "", errors.New("the update was accepted but the server's url does not match")
	}
	log.Info("updated %s", ch.Name)
	return SyncUpdated, nil
}

func findResolver(channels []Channel, resolver string) (*Channel, *Source) {
	for i := range channels {
		for j := range channels[i].Sources {
			if channels[i].Sources[j].Resolver == resolver {
				return &channels[i], &channels[i].Sources[j]
			}
		}
	}
	return nil, nil
}

func sourceByID(list []Source, id string) *Source {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

func expiry(u string) string {
	if exp, ok := stream.ExpiresAt(u); ok {
		return time.Unix(exp, 0).UTC().Format(time.RFC3339)
	}
	return "unknown"
}
