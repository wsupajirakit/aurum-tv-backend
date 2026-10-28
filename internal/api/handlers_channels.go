package api

import (
	"net/http"

	"aurumtv/internal/channel"
	"aurumtv/internal/httpx"
)

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) error {
	// Renew resolver sources that are about to expire before answering.
	s.Refresher.EnsureFresh(r.Context())

	q := r.URL.Query()
	f := channel.Filter{Query: q.Get("q"), Group: q.Get("group")}
	if q.Has("enabled") {
		enabled := q.Get("enabled") != "false"
		f.Enabled = &enabled
	}
	m := s.Channels.Meta()
	httpx.JSON(w, http.StatusOK, map[string]any{"count": m.Count, "updatedAt": m.UpdatedAt, "version": m.Version,
		"groups": s.Channels.Groups(), "channels": s.presentAll(r, s.Channels.List(f))})
	return nil
}

func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	ch := s.Channels.Get(id)
	if ch == nil {
		return notFoundChannel()
	}
	if r.URL.Query().Get("refresh") == "true" {
		if fresh := s.Refresher.ForceRefresh(r.Context(), id); fresh != nil {
			ch = fresh
		}
	}
	httpx.JSON(w, http.StatusOK, s.present(r, *ch))
	return nil
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) error {
	p, err := channelInput(r, true)
	if err != nil {
		return err
	}
	ch, err := s.Channels.Create(patchToRaw(p))
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, s.present(r, *ch))
	return nil
}

func (s *Server) update(w http.ResponseWriter, r *http.Request, full bool) error {
	p, err := channelInput(r, full)
	if err != nil {
		return err
	}
	ch, err := s.Channels.Update(r.PathValue("id"), p)
	if err != nil {
		return err
	}
	if ch == nil {
		return notFoundChannel()
	}
	httpx.JSON(w, http.StatusOK, s.present(r, *ch))
	return nil
}

func (s *Server) replaceChannel(w http.ResponseWriter, r *http.Request) error {
	return s.update(w, r, true)
}
func (s *Server) patchChannel(w http.ResponseWriter, r *http.Request) error {
	return s.update(w, r, false)
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) error {
	ch, err := s.Channels.Remove(r.PathValue("id"))
	if err != nil {
		return err
	}
	if ch == nil {
		return notFoundChannel()
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"deleted": true, "channel": s.present(r, *ch)})
	return nil
}

func (s *Server) bulkChannels(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Channels []rawJSON `json:"channels"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil || len(body.Channels) < 1 || len(body.Channels) > 500 {
		return invalid(Issue{Path: "channels", Message: "must be an array of 1 to 500 channels"})
	}
	items := make([]channel.Raw, 0, len(body.Channels))
	for _, raw := range body.Channels {
		p, err := channelInput(httpx.WithBody(r, raw), true)
		if err != nil {
			return err
		}
		items = append(items, patchToRaw(p))
	}
	created, err := s.Channels.BulkCreate(items)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"added": len(created), "channels": s.presentAll(r, created)})
	return nil
}

func (s *Server) reorderChannels(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil || len(body.IDs) == 0 {
		return invalid(Issue{Path: "ids", Message: "must be a non-empty array of ids"})
	}
	for _, id := range body.IDs {
		if id == "" {
			return invalid(Issue{Path: "ids", Message: "ids must not be empty"})
		}
	}
	list, err := s.Channels.Reorder(body.IDs)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"channels": s.presentAll(r, list)})
	return nil
}
