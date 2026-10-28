package ops

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"aurumtv/internal/channel"
	"aurumtv/internal/hls"
	"aurumtv/internal/platform/pool"
)

// DefaultRules are the built-in discovery rules; --rules <file> replaces them.
//
//go:embed discovery-rules.json
var DefaultRules []byte

// Rule turns a known source url into candidate urls.
//   - Match: regexp over the existing url
//   - Template: {1},{2}… are regexp groups, {n} is each number of Range
//   - Label: label of a source found by this rule
type Rule struct {
	Name     string `json:"name"`
	Match    string `json:"match"`
	Template string `json:"template"`
	Range    [2]int `json:"range"`
	Label    string `json:"label"`
	re       *regexp.Regexp
}

const maxRangeSpan = 50

// ParseRules validates a rules document.
func ParseRules(data []byte) ([]Rule, error) {
	var doc struct{ Rules []Rule }
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: discovery rules: %v", ErrConfig, err)
	}
	for i := range doc.Rules {
		r := &doc.Rules[i]
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return nil, fmt.Errorf("%w: rule %q: %v", ErrConfig, r.Name, err)
		}
		if r.Range[0] > r.Range[1] || r.Range[1]-r.Range[0] > maxRangeSpan {
			return nil, fmt.Errorf("%w: rule %q: range must be ascending and span at most %d", ErrConfig, r.Name, maxRangeSpan)
		}
		r.re = re
	}
	return doc.Rules, nil
}

// LoadRules reads a rules file, or the built-in rules when file is "".
func LoadRules(file string) ([]Rule, error) {
	if file == "" {
		return ParseRules(DefaultRules)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConfig, err)
	}
	return ParseRules(data)
}

var placeholder = regexp.MustCompile(`\{(\d+|n)\}`)

type candidate struct {
	url  string
	rule *Rule
}

func candidatesFor(u string, rules []Rule) []candidate {
	var out []candidate
	for i := range rules {
		r := &rules[i]
		m := r.re.FindStringSubmatch(u)
		if m == nil {
			continue
		}
		for n := r.Range[0]; n <= r.Range[1]; n++ {
			c := placeholder.ReplaceAllStringFunc(r.Template, func(tok string) string {
				k := tok[1 : len(tok)-1]
				if k == "n" {
					return strconv.Itoa(n)
				}
				if g, _ := strconv.Atoi(k); g < len(m) {
					return m[g]
				}
				return ""
			})
			out = append(out, candidate{url: c, rule: r})
		}
	}
	return out
}

func label(r hls.Result, rule *Rule) string {
	if _, h, ok := strings.Cut(r.Resolution, "x"); ok && h != "" {
		return rule.Label + " " + h + "p"
	}
	return rule.Label
}

// DiscoverOptions controls Discover.
type DiscoverOptions struct {
	Apply       bool
	ChannelID   string
	Rules       []Rule
	Concurrency int
	Probe       func(ctx context.Context, url string) hls.Result
}

// Discover finds backup sources for every channel from the rules. Nothing is guessed: a candidate is
// reported (and, with Apply, appended) only after it plays end to end — playlist → variant → a real
// media segment. Existing sources are never removed or reordered.
func Discover(ctx context.Context, admin *Admin, o DiscoverOptions, log Logger) error {
	channels, err := admin.ListChannels(ctx)
	if err != nil {
		return err
	}
	added := 0
	var noBackup []string
	for _, ch := range channels {
		if o.ChannelID != "" && ch.ID != o.ChannelID {
			continue
		}
		known := map[string]bool{}
		for _, s := range ch.Sources {
			known[s.URL] = true
		}
		var cands []candidate
		for _, s := range ch.Sources {
			if s.URL == "" {
				continue
			}
			for _, c := range candidatesFor(s.URL, o.Rules) {
				if !known[c.url] {
					known[c.url] = true
					cands = append(cands, c)
				}
			}
		}
		results := pool.Run(cands, o.Concurrency, func(c candidate) hls.Result { return o.Probe(ctx, c.url) })
		room := channel.MaxSourcesPerChan - len(ch.Sources)
		var verified []Source
		next := -1
		ids := map[string]bool{}
		for _, s := range ch.Sources {
			next = max(next, s.Priority)
			ids[s.ID] = true
		}
		for i, c := range cands {
			if !results[i].OK || len(verified) >= room {
				continue
			}
			next++
			id := "found-" + strconv.Itoa(next)
			for ids[id] {
				id += "x"
			}
			ids[id] = true
			verified = append(verified, Source{ID: id, Label: label(results[i], c.rule), URL: c.url, Resolver: channel.ResolverStatic, Priority: next})
		}
		log.Info("%s: %d existing · %d candidates · %d verified", ch.Name, len(ch.Sources), len(cands), len(verified))
		for _, v := range verified {
			log.Info("   + %s (%s)", v.URL, v.Label)
		}
		if len(ch.Sources)+len(verified) < 2 {
			noBackup = append(noBackup, ch.Name)
		}
		if !o.Apply || len(verified) == 0 {
			continue
		}
		if _, err := admin.PatchSources(ctx, ch.ID, append(append([]Source(nil), ch.Sources...), verified...)); err != nil {
			return err
		}
		added += len(verified)
	}
	if len(noBackup) > 0 {
		log.Warn("still without a working backup: %s — add one with PATCH /api/channels/{id}", strings.Join(noBackup, ", "))
	}
	if o.Apply {
		log.Info("added %d backup source(s)", added)
	} else {
		log.Info("report only — pass --apply to add them")
	}
	return nil
}
