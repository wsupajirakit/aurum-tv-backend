package api

import (
	"net/http"

	"aurumtv/internal/httpx"
	"aurumtv/internal/stream"
)

// refreshChannel renews the channel's resolver sources now.
func (s *Server) refreshChannel(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	ch := s.Channels.Get(id)
	if ch == nil {
		return notFoundChannel()
	}
	if fresh := s.Refresher.ForceRefresh(r.Context(), id); fresh != nil {
		ch = fresh
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "channel": s.present(r, *ch)})
	return nil
}

// syncChannel: the app pushes a freshly resolved link so other TVs get it too. Three guards:
// the app signature (route), the channel must have a resolver source, and the url must be an
// https .m3u8 on an allowed host.
func (s *Server) syncChannel(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	ch := s.Channels.Get(id)
	if ch == nil {
		return notFoundChannel()
	}
	src := ch.ResolverSource()
	if src == nil {
		s.Log.Warn("sync refused: channel has no resolver source", "channel", ch.Name)
		httpx.JSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "this channel does not accept synced links", "code": "not_dynamic"})
		return nil
	}
	var body struct {
		URL string `json:"url"`
	}
	_ = httpx.DecodeJSON(r, &body)
	reason := "missing_url"
	var clean string
	if body.URL != "" && len(body.URL) <= 2048 {
		clean, reason = stream.ValidateSyncURL(body.URL, s.cfg.Stream.SyncAllowedHosts)
	}
	if reason != "" {
		s.Log.Warn("sync refused", "channel", ch.Name, "reason", reason)
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid url", "code": reason})
		return nil
	}
	updated, _, err := s.Channels.UpdateSourceURL(id, src.ID, clean)
	if err != nil {
		return err
	}
	if updated == nil {
		updated = s.Channels.Get(id)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "channel": s.present(r, *updated)})
	return nil
}

func (s *Server) channelSources(w http.ResponseWriter, r *http.Request) error {
	ch := s.Channels.Get(r.PathValue("id"))
	if ch == nil {
		return notFoundChannel()
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"channelId": ch.ID, "activeSourceId": ch.ActiveSourceID,
		"sources": s.Monitor.Describe(*ch)})
	return nil
}

// reportPlayback: the app could not play the channel → expedite a real check. Nothing switches
// on the report alone; a cooldown limits reports per channel.
func (s *Server) reportPlayback(w http.ResponseWriter, r *http.Request) error {
	ch := s.Channels.Get(r.PathValue("id"))
	if ch == nil {
		return notFoundChannel()
	}
	m, err := fields(r)
	if err != nil {
		return err
	}
	rd := &reader{m: m}
	if id := rd.str("sourceId", 1, 64); id != nil && !sourceIDPattern.MatchString(*id) {
		rd.add("sourceId", "invalid source id")
	}
	rd.str("url", 0, 2048)
	rd.str("reason", 0, 120)
	if len(rd.issues) > 0 {
		return invalid(rd.issues...)
	}
	accepted := s.Monitor.Report(ch.ID)
	status := http.StatusOK
	if accepted {
		status = http.StatusAccepted
	}
	httpx.JSON(w, status, map[string]any{"ok": true, "accepted": accepted})
	return nil
}

func (s *Server) probeChannel(w http.ResponseWriter, r *http.Request) error {
	ch := s.Channels.Get(r.PathValue("id"))
	if ch == nil {
		return notFoundChannel()
	}
	res := s.Monitor.RunCycle(r.Context(), ch.ID, true)
	fresh := s.Channels.Get(ch.ID)
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "switches": res.Switches,
		"activeSourceId": fresh.ActiveSourceID, "sources": s.Monitor.Describe(*fresh)})
	return nil
}

func (s *Server) sourcesOverview(w http.ResponseWriter, _ *http.Request) error {
	type src struct {
		ID            string  `json:"id"`
		Label         string  `json:"label"`
		Active        bool    `json:"active"`
		Status        string  `json:"status"`
		LastCheckedAt *string `json:"lastCheckedAt"`
		LastResult    any     `json:"lastResult"`
	}
	type item struct {
		ID             string  `json:"id"`
		Name           string  `json:"name"`
		ActiveSourceID *string `json:"activeSourceId"`
		Sources        []src   `json:"sources"`
	}
	out := []item{}
	for _, ch := range s.Channels.List(channelFilterAll) {
		it := item{ID: ch.ID, Name: ch.Name, ActiveSourceID: ch.ActiveSourceID, Sources: []src{}}
		for _, v := range s.Monitor.Describe(ch) {
			it.Sources = append(it.Sources, src{ID: v.ID, Label: v.Label, Active: v.Active, Status: v.Health.Status,
				LastCheckedAt: v.Health.LastCheckedAt, LastResult: v.Health.LastResult})
		}
		out = append(out, it)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"channels": out})
	return nil
}
