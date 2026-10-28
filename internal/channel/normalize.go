package channel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

var sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func isResolver(kind string) bool { return kind == ResolverStatic || kind == ResolverCh3Plus }

// NormalizeSources fixes up a source list:
//   - no sources but a url → one static source "primary" (channels created with a bare url)
//   - invalid or duplicate ids are replaced; unknown resolvers become static
//   - sources without a url are dropped unless they resolve their own url
//   - at most MaxSourcesPerChan, sorted by priority (lower = preferred)
func NormalizeSources(raw Raw) []Source {
	list := raw.Sources
	if len(list) == 0 && strings.TrimSpace(raw.URL) != "" {
		list = []RawSource{{ID: legacyPrimaryID, URL: raw.URL, Resolver: ResolverStatic, Priority: ptr(0.0)}}
	}
	seen := map[string]bool{}
	out := make([]Source, 0, len(list))
	for i, s := range list {
		id := strings.TrimSpace(s.ID)
		if !sourceIDPattern.MatchString(id) || seen[id] {
			id = fmt.Sprintf("src-%d", i+1)
		}
		for seen[id] {
			id += "x"
		}
		seen[id] = true
		resolver := s.Resolver
		if !isResolver(resolver) {
			resolver = ResolverStatic
		}
		priority := i
		if s.Priority != nil && !math.IsNaN(*s.Priority) && !math.IsInf(*s.Priority, 0) {
			priority = int(*s.Priority)
		}
		src := Source{ID: id, Label: strings.TrimSpace(s.Label), URL: strings.TrimSpace(s.URL), Resolver: resolver, Priority: priority}
		if src.URL == "" && src.Resolver == ResolverStatic {
			continue
		}
		out = append(out, src)
	}
	if len(out) > MaxSourcesPerChan {
		out = out[:MaxSourcesPerChan]
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Priority < out[b].Priority })
	return out
}

// pickActiveSourceID: the requested id if it still exists, else the source whose url matches,
// else the first source.
func pickActiveSourceID(sources []Source, raw Raw) *string {
	for _, s := range sources {
		if raw.ActiveSourceID != "" && s.ID == raw.ActiveSourceID {
			return ptr(s.ID)
		}
	}
	for _, s := range sources {
		if s.URL != "" && s.URL == strings.TrimSpace(raw.URL) {
			return ptr(s.ID)
		}
	}
	if len(sources) > 0 {
		return ptr(sources[0].ID)
	}
	return nil
}

func validNumber(n *float64) *int {
	if n == nil || *n != math.Trunc(*n) || *n < 1 || *n > MaxChannelNumber {
		return nil
	}
	return ptr(int(*n))
}

// Normalize turns any input into a complete channel; index is the fallback order.
func Normalize(raw Raw, index int, now time.Time) Channel {
	sources := NormalizeSources(raw)
	active := pickActiveSourceID(sources, raw)
	url := strings.TrimSpace(raw.URL)
	if url == "" && active != nil {
		for _, s := range sources {
			if s.ID == *active {
				url = s.URL
			}
		}
	}
	order := index
	if raw.Order != nil && !math.IsNaN(*raw.Order) && !math.IsInf(*raw.Order, 0) {
		order = int(*raw.Order)
	}
	enabled := true
	if raw.Enabled != nil {
		enabled = *raw.Enabled
	}
	stamp := now.UTC().Format(isoLayout)
	ch := Channel{
		ID: raw.ID, Name: strings.TrimSpace(raw.Name), URL: url, Logo: strings.TrimSpace(raw.Logo),
		Group: strings.TrimSpace(raw.Group), Number: validNumber(raw.Number), Enabled: enabled, Order: order,
		Sources: sources, ActiveSourceID: active, CreatedAt: raw.CreatedAt, UpdatedAt: raw.UpdatedAt,
	}
	if ch.ID == "" {
		ch.ID = newUUID()
	}
	if ch.CreatedAt == "" {
		ch.CreatedAt = stamp
	}
	if ch.UpdatedAt == "" {
		ch.UpdatedAt = stamp
	}
	return ch
}

// isoLayout matches JavaScript's Date.toISOString (millisecond precision, Z).
const isoLayout = "2006-01-02T15:04:05.000Z"

func ISO(t time.Time) string { return t.UTC().Format(isoLayout) }

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
