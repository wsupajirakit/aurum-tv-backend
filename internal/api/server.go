// Package api wires every HTTP route: middleware per route, handlers per area.
package api

import (
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"

	"aurumtv/internal/auth"
	"aurumtv/internal/auth/appauth"
	"aurumtv/internal/channel"
	"aurumtv/internal/config"
	"aurumtv/internal/httpx"
	"aurumtv/internal/logs"
	"aurumtv/internal/middleware"
	"aurumtv/internal/platform/clock"
	"aurumtv/internal/session"
	"aurumtv/internal/source"
	"aurumtv/internal/source/resolver"
)

// ServiceName is shown on the root endpoint and in startup logs.
const ServiceName = "Aurum TV API"

// Deps are the services the HTTP layer uses.
type Deps struct {
	Config    *config.Config
	Clock     clock.Clock
	Log       *slog.Logger
	Channels  *channel.Repository
	Sessions  *session.Service
	Tokens    *auth.Tokens
	APIKeys   *auth.APIKeys
	Guard     *appauth.Guard
	Health    *source.Store
	Monitor   *source.Monitor
	Refresher *source.Refresher
	Resolvers resolver.Registry
	Logs      *logs.Service
	RateLimit *middleware.RateLimit
	// HTTPClient is used for outbound calls (diagnostics).
	HTTPClient *http.Client
}

type Server struct {
	Deps
	cfg          *config.Config
	health       *source.Store
	mux          *http.ServeMux
	eventStreams atomic.Int32
}

// handlerFunc returns an error instead of writing it; errors become {error, code} responses.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (s *Server) wrap(h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			var ve *validationError
			if errors.As(err, &ve) {
				httpx.JSON(w, http.StatusBadRequest, httpx.ErrorBody("invalid_request", "request validation failed",
					map[string]any{"issues": ve.issues}))
				return
			}
			var he *httpx.Error
			if !errors.As(err, &he) {
				s.Log.Error("handler error", "path", r.URL.Path, "err", err)
			}
			httpx.WriteError(w, err)
		}
	})
}

type mw = func(http.Handler) http.Handler

func (s *Server) route(pattern string, h handlerFunc, mws ...mw) {
	var handler http.Handler = s.wrap(h)
	for i := len(mws) - 1; i >= 0; i-- {
		handler = mws[i](handler)
	}
	s.mux.Handle(pattern, handler)
}

// New builds the full HTTP handler.
func New(d Deps) http.Handler {
	s := &Server{Deps: d, cfg: d.Config, health: d.Health, mux: http.NewServeMux()}
	cfg := d.Config

	guardRead := appauth.GuardRead(cfg.PublicRead, d.APIKeys)
	adminKey := appauth.RequireAPIKey(d.APIKeys)
	adminJWT := appauth.RequireAdminJWT(d.Tokens)
	appSigned := d.Guard.Require(false) // session endpoints: signature only (no token yet)
	appAuth := d.Guard.Require(true)
	appRead := d.Guard.ForRead()

	s.route("GET /{$}", s.root)
	s.route("GET /api/health", s.healthz)

	s.route("POST /api/auth/token", s.adminToken)
	s.route("POST /api/session", s.startSession, appSigned)
	s.route("POST /api/session/refresh", s.refreshSession, appSigned)

	s.route("GET /api/channels", s.listChannels, guardRead, appRead)
	s.route("GET /api/channels/{id}", s.getChannel, guardRead, s.readOrRefreshAuth(appRead, appAuth))
	s.route("POST /api/channels/{id}/refresh", s.refreshChannel, guardRead, appAuth)
	s.route("POST /api/channels/{id}/sync", s.syncChannel, appAuth)
	s.route("GET /api/channels/{id}/sources", s.channelSources, appRead)
	s.route("POST /api/channels/{id}/report", s.reportPlayback, appAuth)
	s.route("POST /api/channels/{id}/probe", s.probeChannel, adminKey)
	s.route("GET /api/health/sources", s.sourcesOverview, adminKey)
	s.route("GET /api/channels/{id}/logo", s.channelLogo)
	s.route("GET /api/avatar", s.avatarSVG)
	s.route("GET /api/events", s.events, guardRead, appRead)

	s.route("POST /api/channels", s.createChannel, adminKey)
	s.route("POST /api/channels/bulk", s.bulkChannels, adminKey)
	s.route("POST /api/channels/reorder", s.reorderChannels, adminKey)
	s.route("PUT /api/channels/{id}", s.replaceChannel, adminKey)
	s.route("PATCH /api/channels/{id}", s.patchChannel, adminKey)
	s.route("DELETE /api/channels/{id}", s.deleteChannel, adminKey)

	s.route("POST /api/logs", s.postLogs, limitBody(cfg.Logs.BodyLimit), appAuth)
	s.route("GET /api/logs", s.getLogs, adminJWT)
	s.route("GET /api/logs/dates", s.logDates, adminJWT)
	s.route("GET /api/logs/config", s.logConfig, adminJWT)
	s.route("POST /api/logs/toggle", s.toggleLogs, adminJWT)
	s.route("DELETE /api/logs/{date}", s.deleteLogs, adminJWT)

	if cfg.EnableDiagnostics {
		s.route("GET /api/test/ch3", s.diagnostics, adminJWT)
	}

	// Unknown routes (and unknown methods) answer like every other error.
	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := s.mux.Handler(r); pattern == "" {
			httpx.Fail(w, http.StatusNotFound, "not_found", "no such endpoint")
			return
		}
		s.mux.ServeHTTP(w, r)
	})

	api := middleware.Chain(notFound, d.RateLimit.Middleware, middleware.BodyLimit(cfg.BodyLimit))
	root := http.NewServeMux()
	root.Handle("/api/", api)
	root.Handle("/", notFound)

	return middleware.Chain(root,
		middleware.Recover(d.Log, cfg.Production),
		middleware.AccessLog(d.Log),
		middleware.SecurityHeaders(),
		middleware.CORS(cfg.CORSOrigins),
		middleware.ServerTime(d.Clock),
	)
}

// readOrRefreshAuth: ?refresh=true makes the server contact the stream source, so it needs full
// app authentication like POST /refresh; a plain read uses the read guard.
func (s *Server) readOrRefreshAuth(read, full mw) mw {
	return func(next http.Handler) http.Handler {
		r, f := read(next), full(next)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Query().Get("refresh") == "true" {
				f.ServeHTTP(w, req)
				return
			}
			r.ServeHTTP(w, req)
		})
	}
}

// limitBody applies a tighter per-route limit on the already buffered body.
func limitBody(limit int64) mw {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if int64(len(httpx.Body(r))) > limit {
				httpx.Fail(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func notFoundChannel() error {
	return httpx.NewError(http.StatusNotFound, "channel_not_found", "channel not found")
}
