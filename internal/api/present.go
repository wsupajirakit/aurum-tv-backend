package api

import (
	"net/http"
	"strings"

	"aurumtv/internal/avatar"
	"aurumtv/internal/channel"
)

type presentedSource struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	URL      string `json:"url"`
	Resolver string `json:"resolver"`
	Priority int    `json:"priority"`
	Status   string `json:"status"`
}

type fallback struct {
	Initial string    `json:"initial"`
	Colors  [2]string `json:"colors"`
}

// presentedChannel is what clients receive: the stored channel plus display helpers and the
// health status of every source (the app ranks backups with it).
type presentedChannel struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	Logo           string            `json:"logo"`
	Group          string            `json:"group"`
	Number         *int              `json:"number"`
	Enabled        bool              `json:"enabled"`
	Order          int               `json:"order"`
	Sources        []presentedSource `json:"sources"`
	ActiveSourceID *string           `json:"activeSourceId"`
	CreatedAt      string            `json:"createdAt"`
	UpdatedAt      string            `json:"updatedAt"`
	HasLogo        bool              `json:"hasLogo"`
	LogoURL        string            `json:"logoUrl"`
	Fallback       fallback          `json:"fallback"`
}

// baseURL builds the public origin; forwarded headers are trusted only behind a proxy.
func (s *Server) baseURL(r *http.Request) string {
	proto, host := "http", r.Host
	if r.TLS != nil {
		proto = "https"
	}
	if s.cfg.TrustProxy {
		if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
			proto = strings.TrimSpace(strings.Split(p, ",")[0])
		}
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = strings.TrimSpace(strings.Split(h, ",")[0])
		}
	}
	return proto + "://" + host
}

func (s *Server) present(r *http.Request, c channel.Channel) presentedChannel {
	sources := make([]presentedSource, len(c.Sources))
	for i, src := range c.Sources {
		sources[i] = presentedSource{ID: src.ID, Label: src.Label, URL: src.URL, Resolver: src.Resolver,
			Priority: src.Priority, Status: s.health.Get(c.ID, src.ID).Status}
	}
	logoURL := c.Logo
	if logoURL == "" {
		logoURL = s.baseURL(r) + "/api/channels/" + c.ID + "/logo"
	}
	return presentedChannel{
		ID: c.ID, Name: c.Name, URL: c.URL, Logo: c.Logo, Group: c.Group, Number: c.Number, Enabled: c.Enabled,
		Order: c.Order, Sources: sources, ActiveSourceID: c.ActiveSourceID, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		HasLogo: c.Logo != "", LogoURL: logoURL, Fallback: fallback{Initial: avatar.Initial(c.Name), Colors: avatar.Colors(c.Name)},
	}
}

func (s *Server) presentAll(r *http.Request, list []channel.Channel) []presentedChannel {
	out := make([]presentedChannel, len(list))
	for i, c := range list {
		out[i] = s.present(r, c)
	}
	return out
}
