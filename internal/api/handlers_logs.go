package api

import (
	"encoding/json"
	"net/http"
	"time"

	"aurumtv/internal/httpx"
	"aurumtv/internal/logs"
	"aurumtv/internal/middleware"
	"aurumtv/internal/stream"
)

// postLogs accepts one log object, an array, or {logs: [...]}.
func (s *Server) postLogs(w http.ResponseWriter, r *http.Request) error {
	var body any
	if err := json.Unmarshal(httpx.Body(r), &body); err != nil {
		return httpx.NewError(http.StatusBadRequest, "invalid_json", "body must be JSON")
	}
	ip := middleware.ClientIP(r, s.cfg.TrustProxy)
	var res logs.AppendResult
	switch v := body.(type) {
	case []any:
		res = s.Logs.AppendBatch(v, ip)
	case map[string]any:
		if list, ok := v["logs"].([]any); ok {
			res = s.Logs.AppendBatch(list, ip)
		} else {
			res = s.Logs.Append(v, ip)
		}
	default:
		return httpx.NewError(http.StatusBadRequest, "invalid_log", "invalid log payload")
	}
	httpx.JSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
		logs.AppendResult
	}{true, res})
	return nil
}

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	page, err := s.Logs.Get(logs.Query{Date: q.Get("date"), Level: q.Get("level"), Tag: q.Get("tag"), State: q.Get("state"),
		Q: q.Get("q"), ChannelID: q.Get("channelId"), Limit: q.Get("limit"), Offset: q.Get("offset"), Order: q.Get("order")})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, page)
	return nil
}

func (s *Server) logDates(w http.ResponseWriter, _ *http.Request) error {
	files := s.Logs.Dates()
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(files), "files": files})
	return nil
}

// logConfig never exposes server paths.
func (s *Server) logConfig(w http.ResponseWriter, _ *http.Request) error {
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": s.Logs.Enabled(), "today": s.Logs.Today()})
	return nil
}

// toggleLogs sets {enabled} or flips the current value when the body has none.
func (s *Server) toggleLogs(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	_ = httpx.DecodeJSON(r, &body)
	next := !s.Logs.Enabled()
	if body.Enabled != nil {
		next = *body.Enabled
	}
	current := s.Logs.SetEnabled(next)
	msg := "log storage disabled"
	if current {
		msg = "log storage enabled"
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": current, "message": msg})
	return nil
}

func (s *Server) deleteLogs(w http.ResponseWriter, r *http.Request) error {
	res, err := s.Logs.Delete(r.PathValue("date"))
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
		*logs.DeleteResult
	}{true, res})
	return nil
}

// diagnostics checks the stream source page from the server (off unless ENABLE_DIAGNOSTICS).
// It never returns HTML or stack traces.
func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) error {
	started := time.Now()
	page, err := stream.FetchPage(r.Context(), s.HTTPClient, s.cfg.Stream.SourceURL, s.cfg.Stream.FetchTimeout)
	if err != nil {
		httpx.JSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error(), "code": "source_unreachable"})
		return nil
	}
	u := stream.ExtractStreamURL(page.HTML)
	var streamURL any
	if u != "" {
		streamURL = u
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "status": page.Status, "fetchTimeMs": time.Since(started).Milliseconds(),
		"htmlLength": len(page.HTML), "foundStream": u != "", "streamUrl": streamURL})
	return nil
}
