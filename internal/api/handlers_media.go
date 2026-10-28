package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"aurumtv/internal/avatar"
	"aurumtv/internal/channel"
	"aurumtv/internal/httpx"
)

var channelFilterAll = channel.Filter{}

const (
	avatarCacheControl = "public, max-age=86400"
	ssePingEvery       = 25 * time.Second
	// maxEventStreams bounds concurrent SSE connections (each holds a goroutine and a socket).
	maxEventStreams = 512
	// maxAvatarName bounds the text echoed into a generated avatar.
	maxAvatarName = 64
)

func sendSVG(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", avatarCacheControl)
	_, _ = w.Write([]byte(avatar.SVG(name)))
}

// channelLogo redirects to the channel's logo, or draws a fallback avatar.
func (s *Server) channelLogo(w http.ResponseWriter, r *http.Request) error {
	ch := s.Channels.Get(r.PathValue("id"))
	if ch == nil {
		return notFoundChannel()
	}
	if ch.Logo != "" {
		http.Redirect(w, r, ch.Logo, http.StatusFound)
		return nil
	}
	sendSVG(w, ch.Name)
	return nil
}

func (s *Server) avatarSVG(w http.ResponseWriter, r *http.Request) error {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "?"
	}
	if runes := []rune(name); len(runes) > maxAvatarName {
		name = string(runes[:maxAvatarName])
	}
	sendSVG(w, name)
	return nil
}

// events streams channel changes (SSE) and pings every 25 s so proxies keep the connection open.
func (s *Server) events(w http.ResponseWriter, r *http.Request) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return httpx.NewError(http.StatusInternalServerError, "streaming_unsupported", "streaming is not supported")
	}
	if s.eventStreams.Add(1) > maxEventStreams {
		s.eventStreams.Add(-1)
		return httpx.NewError(http.StatusServiceUnavailable, "too_many_streams", "too many live connections — try again later")
	}
	defer s.eventStreams.Add(-1)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // the server write timeout must not cut a live stream

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	events, cancel := s.Channels.Subscribe(32)
	defer cancel()
	write := func(event string, data any) error {
		var payload []byte
		if str, isStr := data.(string); isStr {
			payload = []byte(str)
		} else {
			payload, _ = json.Marshal(data)
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if err := write("ready", s.Channels.Meta()); err != nil {
		return nil
	}
	ping := time.NewTicker(ssePingEvery)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case e, open := <-events:
			if !open || write(e.Type, e) != nil {
				return nil
			}
		case <-ping.C:
			if write("ping", strconv.FormatInt(time.Now().UnixMilli(), 10)) != nil {
				return nil
			}
		}
	}
}
