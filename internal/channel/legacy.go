package channel

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Per-channel rules of the v1 system. They are used ONLY to migrate v1 data once; everything else
// reads channel behaviour from data (sources[].resolver, order, number).

var (
	legacyDynamicName = regexp.MustCompile(`(?i)^(3\s*hd|ช่อง\s*3|ch\s*3|channel\s*3)($|\s)`)
	digitsPattern     = regexp.MustCompile(`\d+`)
	thaiPBSPattern    = regexp.MustCompile(`(?i)thai\s*pbs`)
)

// v1 forced Channel 3 to position 3 at response time.
const legacyDynamicDisplayIndex = 2

func isLegacyDynamic(r Raw, legacyIDs []string) bool {
	for _, id := range legacyIDs {
		if r.ID == id {
			return true
		}
	}
	return legacyDynamicName.MatchString(strings.TrimSpace(r.Name))
}

// legacyChannelNumber is the last number in the name (Thai PBS = 3). Using the last number fixes
// the v1 bug where "True4U 24" showed 4.
func legacyChannelNumber(name string) *float64 {
	if all := digitsPattern.FindAllString(name, -1); len(all) > 0 {
		n, _ := strconv.ParseFloat(all[len(all)-1], 64)
		return &n
	}
	if thaiPBSPattern.MatchString(name) {
		return ptr(3.0)
	}
	return nil
}

// migrateV1toV2 moves rules that used to live in code into data; what users see stays the same:
//   - single url → sources[] (Channel 3 gets a ch3plus resolver source)
//   - the avatar number → number
//   - "Channel 3 at position 3" → a real order
func migrateV1toV2(channels []Raw, legacyIDs []string) []Raw {
	out := make([]Raw, len(channels))
	copy(out, channels)
	orderOf := func(r Raw) float64 {
		if r.Order == nil {
			return 0
		}
		return *r.Order
	}
	sort.SliceStable(out, func(a, b int) bool { return orderOf(out[a]) < orderOf(out[b]) })

	for i := range out {
		r := &out[i]
		if len(r.Sources) == 0 {
			src := RawSource{ID: legacyPrimaryID, URL: r.URL, Resolver: ResolverStatic, Priority: ptr(0.0)}
			if isLegacyDynamic(*r, legacyIDs) {
				src.ID, src.Resolver = legacyResolverID, ResolverCh3Plus
			}
			r.Sources = []RawSource{src}
		}
		if r.Number == nil {
			r.Number = legacyChannelNumber(r.Name)
		}
	}
	if idx := indexOf(out, func(r Raw) bool { return isLegacyDynamic(r, legacyIDs) }); idx != -1 && len(out) > legacyDynamicDisplayIndex {
		dynamic := out[idx]
		out = append(out[:idx], out[idx+1:]...)
		out = append(out[:legacyDynamicDisplayIndex], append([]Raw{dynamic}, out[legacyDynamicDisplayIndex:]...)...)
	}
	for i := range out {
		out[i].Order = ptr(float64(i))
	}
	return out
}

// Migrate upgrades raw file data to the current schema; it returns the steps applied.
func Migrate(version int, channels []Raw, legacyIDs []string) (int, []Raw, []string) {
	if version < 1 {
		version = 1
	}
	var applied []string
	if version == 1 {
		channels = migrateV1toV2(channels, legacyIDs)
		version = 2
		applied = append(applied, "v1→v2")
	}
	return version, channels, applied
}

func indexOf(list []Raw, pred func(Raw) bool) int {
	for i, r := range list {
		if pred(r) {
			return i
		}
	}
	return -1
}
