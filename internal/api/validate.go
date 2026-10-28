package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"

	"aurumtv/internal/channel"
	"aurumtv/internal/httpx"
)

// Issue is one validation problem, returned to admin clients.
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type validationError struct{ issues []Issue }

func (v *validationError) Error() string { return fmt.Sprintf("%d validation issue(s)", len(v.issues)) }

func invalid(issues ...Issue) error { return &validationError{issues: issues} }

var (
	httpURLPattern  = regexp.MustCompile(`(?i)^https?://`)
	sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	devicePattern   = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
)

// fields decodes a JSON object body into raw fields (unknown keys are simply ignored later).
func fields(r *http.Request) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := httpx.DecodeJSON(r, &m); err != nil || m == nil {
		return nil, invalid(Issue{Path: "", Message: "body must be a JSON object"})
	}
	return m, nil
}

type reader struct {
	m      map[string]json.RawMessage
	issues []Issue
	prefix string
}

func (rd *reader) add(path, msg string) {
	rd.issues = append(rd.issues, Issue{Path: rd.prefix + path, Message: msg})
}

func (rd *reader) has(k string) bool { _, ok := rd.m[k]; return ok }

func (rd *reader) str(k string, min, max int) *string {
	raw, ok := rd.m[k]
	if !ok {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		rd.add(k, "must be a string")
		return nil
	}
	s = strings.TrimSpace(s)
	if len([]rune(s)) < min {
		rd.add(k, fmt.Sprintf("must be at least %d character(s)", min))
		return nil
	}
	if max > 0 && len([]rune(s)) > max {
		rd.add(k, fmt.Sprintf("must be at most %d characters", max))
		return nil
	}
	return &s
}

// httpURL: a trimmed http(s) url; allowEmpty accepts "".
func (rd *reader) httpURL(k string, allowEmpty bool) *string {
	raw, ok := rd.m[k]
	if !ok {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		rd.add(k, "must be a string")
		return nil
	}
	s = strings.TrimSpace(s)
	if s == "" && allowEmpty {
		return &s
	}
	if !httpURLPattern.MatchString(s) {
		rd.add(k, "must start with http:// or https://")
		return nil
	}
	return &s
}

func (rd *reader) integer(k string, min, max float64) (*float64, bool) {
	raw, ok := rd.m[k]
	if !ok {
		return nil, false
	}
	if string(raw) == "null" {
		return nil, true
	}
	var n float64
	if json.Unmarshal(raw, &n) != nil || n != math.Trunc(n) || n < min || n > max {
		rd.add(k, fmt.Sprintf("must be an integer between %.0f and %.0f", min, max))
		return nil, false
	}
	return &n, true
}

func (rd *reader) boolean(k string) *bool {
	raw, ok := rd.m[k]
	if !ok {
		return nil
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		rd.add(k, "must be a boolean")
		return nil
	}
	return &b
}

func (rd *reader) sources(k string) *[]channel.RawSource {
	raw, ok := rd.m[k]
	if !ok {
		return nil
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		rd.add(k, "must be an array of objects")
		return nil
	}
	if len(items) < 1 || len(items) > channel.MaxSourcesPerChan {
		rd.add(k, fmt.Sprintf("must have 1 to %d sources", channel.MaxSourcesPerChan))
		return nil
	}
	out := make([]channel.RawSource, 0, len(items))
	for i, item := range items {
		sr := &reader{m: item, prefix: fmt.Sprintf("%s%s.%d.", rd.prefix, k, i)}
		var src channel.RawSource
		if id := sr.str("id", 1, 64); id != nil {
			if !sourceIDPattern.MatchString(*id) {
				sr.add("id", "may only contain A-Z a-z 0-9 . _ -")
			}
			src.ID = *id
		}
		if label := sr.str("label", 0, 60); label != nil {
			src.Label = *label
		}
		if u := sr.httpURL("url", true); u != nil {
			src.URL = *u
		}
		if res := sr.str("resolver", 0, 0); res != nil {
			if *res != channel.ResolverStatic && *res != channel.ResolverCh3Plus {
				sr.add("resolver", "must be static or ch3plus")
			}
			src.Resolver = *res
		}
		if p, _ := sr.integer("priority", 0, math.MaxInt32); p != nil {
			src.Priority = p
		}
		if src.URL == "" && (src.Resolver == "" || src.Resolver == channel.ResolverStatic) {
			sr.add("url", "a static source needs a url")
		}
		rd.issues = append(rd.issues, sr.issues...)
		out = append(out, src)
	}
	return &out
}

// channelInput validates a channel body. full=true (POST/PUT) applies defaults (logo "", group "",
// enabled true) and requires a name; partial (PATCH) needs at least one known field.
func channelInput(r *http.Request, full bool) (channel.Patch, error) {
	m, err := fields(r)
	if err != nil {
		return channel.Patch{}, err
	}
	rd := &reader{m: m}
	var p channel.Patch
	p.Name = rd.str("name", 1, 120)
	if full && !rd.has("name") {
		rd.add("name", "is required")
	}
	p.URL = rd.httpURL("url", false)
	p.Logo = rd.httpURL("logo", true)
	if g := rd.str("group", 0, 60); g != nil {
		p.Group = g
	}
	if n, set := rd.integer("number", 1, channel.MaxChannelNumber); set {
		p.Number, p.NumberSet = n, true
	}
	p.Enabled = rd.boolean("enabled")
	if o, _ := rd.integer("order", 0, math.MaxInt32); o != nil {
		p.Order = o
	}
	p.Sources = rd.sources("sources")
	if id := rd.str("activeSourceId", 1, 64); id != nil {
		if !sourceIDPattern.MatchString(*id) {
			rd.add("activeSourceId", "may only contain A-Z a-z 0-9 . _ -")
		}
		p.ActiveSourceID = id
	}
	if len(rd.issues) > 0 {
		return p, invalid(rd.issues...)
	}
	if full {
		empty := ""
		if p.Logo == nil {
			p.Logo = &empty
		}
		if p.Group == nil {
			p.Group = &empty
		}
		if p.Enabled == nil {
			t := true
			p.Enabled = &t
		}
		if (p.URL == nil || *p.URL == "") && (p.Sources == nil || len(*p.Sources) == 0) {
			return p, invalid(Issue{Path: "url", Message: "a url or at least one source is required"})
		}
	} else if p.Name == nil && p.URL == nil && p.Logo == nil && p.Group == nil && !p.NumberSet &&
		p.Enabled == nil && p.Order == nil && p.Sources == nil && p.ActiveSourceID == nil {
		return p, invalid(Issue{Path: "", Message: "send at least one field"})
	}
	return p, nil
}

// patchToRaw turns a validated full input into a create input.
func patchToRaw(p channel.Patch) channel.Raw {
	r := channel.Raw{Enabled: p.Enabled, Order: p.Order, Number: p.Number}
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&r.Name, p.Name)
	set(&r.URL, p.URL)
	set(&r.Logo, p.Logo)
	set(&r.Group, p.Group)
	set(&r.ActiveSourceID, p.ActiveSourceID)
	if p.Sources != nil {
		r.Sources = *p.Sources
	}
	return r
}
