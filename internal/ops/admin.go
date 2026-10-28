package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const maxResponseBytes = 16 << 20

// localHTTP is the only plain-http origin allowed: the admin key must never travel unencrypted.
var localHTTP = regexp.MustCompile(`(?i)^http://(localhost|127\.0\.0\.1|\[::1\])(:\d+)?$`)

// Admin is a minimal client for the admin endpoints.
type Admin struct {
	base   string
	key    string
	client *http.Client
}

func NewAdmin(base, key string, timeout time.Duration) (*Admin, error) {
	base = strings.TrimRight(base, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !localHTTP.MatchString(base)) {
		return nil, fmt.Errorf("%w: TV_API_URL must be https (localhost excepted) — the admin key never travels over http", ErrConfig)
	}
	return &Admin{base: base, key: key, client: &http.Client{Timeout: timeout}}, nil
}

// APIError is a non-2xx answer from the backend.
type APIError struct {
	Method, Path string
	Status       int
	Code         string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s → HTTP %d (%s)", e.Method, e.Path, e.Status, e.Code)
}

func (a *Admin) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", a.key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && ue.Timeout() {
			return fmt.Errorf("%s %s → timed out after %s", method, path, a.client.Timeout)
		}
		return fmt.Errorf("%s %s → %w", method, path, errors.Unwrap(err))
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var e struct{ Code, Error string }
		_ = json.Unmarshal(raw, &e)
		code := e.Code
		if code == "" {
			code = "unknown"
		}
		return &APIError{Method: method, Path: path, Status: res.StatusCode, Code: code}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Source / Channel are the fields the tools need from the API.
type Source struct {
	ID         string      `json:"id"`
	Label      string      `json:"label"`
	URL        string      `json:"url"`
	Resolver   string      `json:"resolver"`
	Priority   int         `json:"priority"`
	Status     string      `json:"status,omitempty"`
	Active     bool        `json:"active,omitempty"`
	LastResult *LastResult `json:"lastResult,omitempty"`
}

type LastResult struct {
	OK         bool    `json:"ok"`
	Stage      string  `json:"stage"`
	Reason     *string `json:"reason"`
	LatencyMs  int64   `json:"latencyMs"`
	Resolution string  `json:"resolution"`
}

type Channel struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Group   string   `json:"group"`
	Sources []Source `json:"sources"`
}

// editable strips presentation-only fields before a PATCH.
func editable(list []Source) []map[string]any {
	out := make([]map[string]any, len(list))
	for i, s := range list {
		out[i] = map[string]any{"id": s.ID, "label": s.Label, "url": s.URL, "resolver": s.Resolver, "priority": s.Priority}
	}
	return out
}

func (a *Admin) ListChannels(ctx context.Context) ([]Channel, error) {
	var out struct{ Channels []Channel }
	err := a.do(ctx, http.MethodGet, "/api/channels", nil, &out)
	return out.Channels, err
}

func (a *Admin) PatchSources(ctx context.Context, id string, sources []Source) (*Channel, error) {
	var out Channel
	err := a.do(ctx, http.MethodPatch, "/api/channels/"+url.PathEscape(id), map[string]any{"sources": editable(sources)}, &out)
	return &out, err
}

func (a *Admin) Probe(ctx context.Context, id string) error {
	return a.do(ctx, http.MethodPost, "/api/channels/"+url.PathEscape(id)+"/probe", nil, nil)
}

func (a *Admin) SourcesOverview(ctx context.Context) ([]Channel, error) {
	var out struct{ Channels []Channel }
	err := a.do(ctx, http.MethodGet, "/api/health/sources", nil, &out)
	return out.Channels, err
}

func (a *Admin) BulkCreate(ctx context.Context, channels any) (int, error) {
	var out struct{ Added int }
	err := a.do(ctx, http.MethodPost, "/api/channels/bulk", map[string]any{"channels": channels}, &out)
	return out.Added, err
}
