// Package appauth authenticates the TV app — two layers controlled by HMAC_MODE:
//  1. an HMAC signature on every request (from our app, untampered, not replayed);
//  2. a session access token (from /api/session, renewed with the refresh token).
//
// enforce (default) rejects with 401 and a code telling the app what to do next;
// log only warns (temporary debugging); off disables both.
package appauth

import (
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"aurumtv/internal/auth"
	"aurumtv/internal/auth/hmacsig"
	"aurumtv/internal/config"
	"aurumtv/internal/httpx"
	"aurumtv/internal/platform/clock"
)

// SessionChecker reports whether a session id is still valid.
type SessionChecker interface{ Active(sessionID string) bool }

type Guard struct {
	mode        string
	requireRead bool
	verifier    *hmacsig.Verifier
	tokens      *auth.Tokens
	sessions    SessionChecker
	apiKeys     *auth.APIKeys
	clock       clock.Clock
	log         *slog.Logger

	warnMu   sync.Mutex
	lastWarn map[string]time.Time
}

type Options struct {
	Mode        string
	RequireRead bool
	Verifier    *hmacsig.Verifier
	Tokens      *auth.Tokens
	Sessions    SessionChecker
	APIKeys     *auth.APIKeys
	Clock       clock.Clock
	Log         *slog.Logger
}

func New(o Options) *Guard {
	return &Guard{mode: o.Mode, requireRead: o.RequireRead, verifier: o.Verifier, tokens: o.Tokens,
		sessions: o.Sessions, apiKeys: o.APIKeys, clock: o.Clock, log: o.Log, lastWarn: map[string]time.Time{}}
}

// reject sends 401 with the server time, so an app with a wrong clock can correct and retry at once.
func (g *Guard) reject(w http.ResponseWriter, code, message string) {
	now := clock.Unix(g.clock)
	w.Header().Set("X-Tv-Server-Time", strconv.FormatInt(now, 10))
	httpx.JSON(w, http.StatusUnauthorized, httpx.ErrorBody(code, message, map[string]any{"serverTime": now}))
}

// softFail logs at most once a minute per kind/reason/path (old clients poll often).
func (g *Guard) softFail(kind, reason, path string) {
	key := kind + ":" + reason + ":" + path
	g.warnMu.Lock()
	defer g.warnMu.Unlock()
	if time.Since(g.lastWarn[key]) < time.Minute {
		return
	}
	g.lastWarn[key] = time.Now()
	g.log.Warn("app auth failed but allowed (HMAC_MODE=log)", "kind", kind, "reason", reason, "path", path)
}

func (g *Guard) accessToken(r *http.Request) string {
	token := auth.BearerToken(r)
	if token == "" {
		return "access_token_missing"
	}
	claims, err := g.tokens.Verify(token)
	if err != nil || claims.Role != auth.RoleDevice || !claims.HasAudience(auth.AudienceTVApp) {
		return "access_token_invalid"
	}
	if !g.sessions.Active(claims.SID) {
		return "session_revoked"
	}
	return ""
}

// Require checks the signature and, when withToken, the access token.
func (g *Guard) Require(withToken bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if g.mode == config.HMACOff {
				next.ServeHTTP(w, r)
				return
			}
			if res := g.verifier.Verify(r); !res.OK {
				if g.mode == config.HMACEnforce {
					g.reject(w, res.Reason, "invalid request signature")
					return
				}
				g.softFail("hmac", res.Reason, r.URL.Path)
			}
			if withToken {
				if reason := g.accessToken(r); reason != "" {
					if g.mode == config.HMACEnforce {
						g.reject(w, reason, "access token is invalid or expired")
						return
					}
					g.softFail("token", reason, r.URL.Path)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ForRead guards list/SSE endpoints when HMAC_REQUIRE_READ is on (default). A valid admin
// X-API-Key also passes, so admin tools can read without the app's HMAC.
func (g *Guard) ForRead() func(http.Handler) http.Handler {
	strict := g.Require(true)
	return func(next http.Handler) http.Handler {
		guarded := strict(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !g.requireRead || g.apiKeys.FromRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			guarded.ServeHTTP(w, r)
		})
	}
}

// RequireAPIKey guards admin endpoints.
func RequireAPIKey(keys *auth.APIKeys) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case !keys.Configured():
				httpx.Fail(w, http.StatusInternalServerError, "api_key_not_configured", "API_KEY is not configured on the server")
			case r.Header.Get(auth.HeaderAPIKey) == "":
				httpx.Fail(w, http.StatusUnauthorized, "api_key_missing", "send the key in the X-API-Key header")
			case !keys.FromRequest(r):
				httpx.Fail(w, http.StatusForbidden, "api_key_invalid", "invalid API key")
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

// GuardRead requires the admin key on reads when PUBLIC_READ=false.
func GuardRead(publicRead bool, keys *auth.APIKeys) func(http.Handler) http.Handler {
	require := RequireAPIKey(keys)
	return func(next http.Handler) http.Handler {
		if publicRead {
			return next
		}
		return require(next)
	}
}

// RequireAdminJWT guards the log endpoints.
func RequireAdminJWT(tokens *auth.Tokens) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := auth.BearerToken(r)
			if token == "" {
				httpx.Fail(w, http.StatusUnauthorized, "token_missing", "send the admin JWT as: Authorization: Bearer <token>")
				return
			}
			claims, err := tokens.Verify(token)
			if err != nil || claims.Role != auth.RoleAdmin {
				httpx.Fail(w, http.StatusUnauthorized, "token_invalid", "admin JWT is invalid or expired")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
