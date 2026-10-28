package ops

import (
	"context"
	"strings"

	"aurumtv/internal/channel"
)

// RestoreChannels re-adds seed channels whose name is missing on the backend (for a wiped data
// volume). It is idempotent: running it again creates nothing new.
func RestoreChannels(ctx context.Context, admin *Admin, dryRun bool, log Logger) error {
	seed, err := channel.Seed()
	if err != nil {
		return err
	}
	channels, err := admin.ListChannels(ctx)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, c := range channels {
		existing[normalizeName(c.Name)] = true
	}
	var missing []channel.Raw
	for _, c := range seed {
		if !existing[normalizeName(c.Name)] {
			missing = append(missing, c)
		}
	}
	log.Info("server has %d channel(s) · seed has %d · missing %d", len(channels), len(seed), len(missing))
	for _, c := range missing {
		log.Info("  + %s (%s)", c.Name, orDash(c.Group))
	}
	if len(missing) == 0 {
		return nil
	}
	if dryRun {
		log.Info("--dry-run: nothing was added")
		return nil
	}
	items := make([]map[string]any, len(missing))
	for i, c := range missing {
		items[i] = bulkItem(c)
	}
	added, err := admin.BulkCreate(ctx, items)
	if err != nil {
		return err
	}
	log.Info("added %d channel(s)", added)
	return nil
}

func normalizeName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// bulkItem keeps only the fields that are set — the API rejects empty strings and nulls.
func bulkItem(c channel.Raw) map[string]any {
	m := map[string]any{"name": c.Name}
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	set("url", c.URL)
	set("logo", c.Logo)
	set("group", c.Group)
	if c.Number != nil {
		m["number"] = *c.Number
	}
	if c.Enabled != nil {
		m["enabled"] = *c.Enabled
	}
	if len(c.Sources) > 0 {
		sources := make([]map[string]any, len(c.Sources))
		for i, s := range c.Sources {
			src := map[string]any{"url": s.URL}
			for k, v := range map[string]string{"id": s.ID, "label": s.Label, "resolver": s.Resolver} {
				if v != "" {
					src[k] = v
				}
			}
			if s.Priority != nil {
				src["priority"] = *s.Priority
			}
			sources[i] = src
		}
		m["sources"] = sources
	}
	return m
}
