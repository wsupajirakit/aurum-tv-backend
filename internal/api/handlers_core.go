package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"aurumtv/internal/httpx"
	"aurumtv/internal/platform/clock"
)

func (s *Server) root(w http.ResponseWriter, _ *http.Request) error {
	endpoints := []string{
		"GET    /api/health",
		"GET    /api/channels",
		"GET    /api/channels/:id",
		"POST   /api/channels/:id/refresh   (renew resolver sources — app signature)",
		"POST   /api/channels/:id/sync      (app pushes a fresh resolver link — app signature)",
		"GET    /api/channels/:id/sources   (source health — app/admin)",
		"POST   /api/channels/:id/report    (app reports a playback failure — app signature)",
		"POST   /api/channels/:id/probe     (probe every source now — admin key)",
		"GET    /api/health/sources         (health overview — admin key)",
		"GET    /api/channels/:id/logo",
		"GET    /api/avatar?name=...",
		"GET    /api/events                 (SSE)",
		"POST   /api/session                (device session — app signature)",
		"POST   /api/session/refresh        (rotate the refresh token)",
		"POST   /api/channels               (admin key)",
		"PUT    /api/channels/:id           (admin key)",
		"PATCH  /api/channels/:id           (admin key)",
		"DELETE /api/channels/:id           (admin key)",
		"POST   /api/channels/bulk          (admin key)",
		"POST   /api/channels/reorder       (admin key)",
		"POST   /api/auth/token             (admin JWT from the API key)",
		"POST   /api/logs                   (app logs — app signature)",
		"GET    /api/logs                   (read logs — admin JWT)",
		"GET    /api/logs/dates             (log files — admin JWT)",
		"GET    /api/logs/config            (log status — admin JWT)",
		"POST   /api/logs/toggle            (enable/disable logs — admin JWT)",
		"DELETE /api/logs/:date             (delete a day — admin JWT)",
	}
	if s.cfg.EnableDiagnostics {
		endpoints = append(endpoints, "GET    /api/test/ch3               (source diagnostics — admin JWT)")
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"name": ServiceName, "docs": "see README.md", "endpoints": endpoints})
	return nil
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) error {
	m := s.Channels.Meta()
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "count": m.Count, "updatedAt": m.UpdatedAt,
		"version": m.Version, "serverTime": clock.Unix(s.Clock)})
	return nil
}

// formatDuration renders 604800s as "7d" (the original response format).
func formatDuration(d time.Duration) string {
	sec := int64(d / time.Second)
	switch {
	case sec%86400 == 0:
		return itoa(sec/86400) + "d"
	case sec%3600 == 0:
		return itoa(sec/3600) + "h"
	default:
		return itoa(sec) + "s"
	}
}

// adminToken trades the API key (X-API-Key header or body {apiKey}) for an admin JWT.
func (s *Server) adminToken(w http.ResponseWriter, r *http.Request) error {
	key := r.Header.Get("X-Api-Key")
	if key == "" {
		var body struct {
			APIKey string `json:"apiKey"`
			Key    string `json:"key"`
		}
		_ = httpx.DecodeJSON(r, &body)
		key = body.APIKey
		if key == "" {
			key = body.Key
		}
	}
	if !s.APIKeys.Valid(key) {
		return httpx.NewError(http.StatusUnauthorized, "api_key_invalid", "invalid API key — send it in X-API-Key or body {apiKey}")
	}
	token, err := s.Tokens.AdminToken()
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "token": token, "tokenType": "Bearer",
		"expiresIn": formatDuration(s.cfg.AdminTokenTTL)})
	return nil
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) error {
	m, err := fields(r)
	if err != nil {
		return err
	}
	rd := &reader{m: m}
	device := rd.str("deviceId", 1, 0)
	if device != nil && !devicePattern.MatchString(*device) {
		rd.add("deviceId", "invalid deviceId")
	}
	if device == nil && !rd.has("deviceId") {
		rd.add("deviceId", "is required")
	}
	platform := rd.str("platform", 0, 32)
	if len(rd.issues) > 0 {
		return invalid(rd.issues...)
	}
	pair, err := s.Sessions.Start(*device, platform)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, pair)
	return nil
}

func (s *Server) refreshSession(w http.ResponseWriter, r *http.Request) error {
	m, err := fields(r)
	if err != nil {
		return err
	}
	rd := &reader{m: m}
	token := rd.str("refreshToken", 16, 256)
	if token == nil && !rd.has("refreshToken") {
		rd.add("refreshToken", "is required")
	}
	if len(rd.issues) > 0 {
		return invalid(rd.issues...)
	}
	pair, err := s.Sessions.Refresh(*token)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, pair)
	return nil
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return strings.TrimSpace(string(b))
}
