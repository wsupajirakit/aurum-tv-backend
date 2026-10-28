package ops

import (
	"context"
	"fmt"
	"io"
)

var statusMark = map[string]string{"up": "UP  ", "down": "DOWN", "unknown": "?   "}

// SourcesStatus prints every channel's sources with their health; probeID (optional) is probed first.
func SourcesStatus(ctx context.Context, admin *Admin, probeID string, out io.Writer) error {
	if probeID != "" {
		if err := admin.Probe(ctx, probeID); err != nil {
			return err
		}
	}
	channels, err := admin.SourcesOverview(ctx)
	if err != nil {
		return err
	}
	for _, ch := range channels {
		fmt.Fprintln(out, ch.Name)
		for _, s := range ch.Sources {
			active := " "
			if s.Active {
				active = ">"
			}
			mark, ok := statusMark[s.Status]
			if !ok {
				mark = statusMark["unknown"]
			}
			fmt.Fprintf(out, "  %s %s %-12s %-16s %s\n", mark, active, s.ID, s.Label, describe(s.LastResult))
		}
	}
	return nil
}

func describe(r *LastResult) string {
	switch {
	case r == nil:
		return "not checked yet"
	case r.OK:
		return fmt.Sprintf("%dms %s", r.LatencyMs, r.Resolution)
	case r.Reason != nil:
		return r.Stage + ":" + *r.Reason
	}
	return r.Stage
}
