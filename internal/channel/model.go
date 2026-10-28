// Package channel stores the channel list: in memory for fast reads, persisted atomically to
// DATA_DIR/channels.json on every change, with change events for live clients.
package channel

// Resolver kinds of a source.
const (
	ResolverStatic  = "static"
	ResolverCh3Plus = "ch3plus"
)

const (
	SchemaVersion     = 2
	MaxSourcesPerChan = 10
	MaxChannelNumber  = 9999
	legacyPrimaryID   = "primary"
	legacyResolverID  = "ch3plus"
)

// Source is one stream source of a channel (primary or backup).
type Source struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	URL      string `json:"url"`
	Resolver string `json:"resolver"`
	Priority int    `json:"priority"`
}

// Channel is the normalized, stored form. URL always mirrors the active source, so clients that
// only read url keep working.
type Channel struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Logo           string   `json:"logo"`
	Group          string   `json:"group"`
	Number         *int     `json:"number"`
	Enabled        bool     `json:"enabled"`
	Order          int      `json:"order"`
	Sources        []Source `json:"sources"`
	ActiveSourceID *string  `json:"activeSourceId"`
	CreatedAt      string   `json:"createdAt"`
	UpdatedAt      string   `json:"updatedAt"`
}

// ResolverSource returns the first source that resolves its own URL, or nil.
func (c *Channel) ResolverSource() *Source {
	for i := range c.Sources {
		if c.Sources[i].Resolver != ResolverStatic {
			return &c.Sources[i]
		}
	}
	return nil
}

func (c *Channel) Source(id string) *Source {
	for i := range c.Sources {
		if c.Sources[i].ID == id {
			return &c.Sources[i]
		}
	}
	return nil
}

func (c *Channel) ActiveID() string {
	if c.ActiveSourceID == nil {
		return ""
	}
	return *c.ActiveSourceID
}

// RawSource / Raw are loose inputs (file data, seed, API patches). Pointers tell "absent" from zero.
type RawSource struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	URL      string   `json:"url"`
	Resolver string   `json:"resolver"`
	Priority *float64 `json:"priority"`
}

type Raw struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	URL            string      `json:"url"`
	Logo           string      `json:"logo"`
	Group          string      `json:"group"`
	Number         *float64    `json:"number"`
	Enabled        *bool       `json:"enabled"`
	Order          *float64    `json:"order"`
	Sources        []RawSource `json:"sources"`
	ActiveSourceID string      `json:"activeSourceId"`
	CreatedAt      string      `json:"createdAt"`
	UpdatedAt      string      `json:"updatedAt"`
}

// State is the channels.json file.
type State struct {
	Version   int       `json:"version"`
	UpdatedAt *string   `json:"updatedAt"`
	Channels  []Channel `json:"channels"`
}

type rawState struct {
	Version   float64 `json:"version"`
	UpdatedAt *string `json:"updatedAt"`
	Channels  []Raw   `json:"channels"`
}

// Raw converts a stored channel back into an input (for merges and re-normalization).
func (c Channel) Raw() Raw {
	r := Raw{ID: c.ID, Name: c.Name, URL: c.URL, Logo: c.Logo, Group: c.Group, Enabled: ptr(c.Enabled),
		Order: ptr(float64(c.Order)), ActiveSourceID: c.ActiveID(), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	if c.Number != nil {
		r.Number = ptr(float64(*c.Number))
	}
	r.Sources = make([]RawSource, len(c.Sources))
	for i, s := range c.Sources {
		r.Sources[i] = RawSource{ID: s.ID, Label: s.Label, URL: s.URL, Resolver: s.Resolver, Priority: ptr(float64(s.Priority))}
	}
	return r
}

func ptr[T any](v T) *T { return &v }
