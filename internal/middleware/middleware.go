// Package middleware holds the HTTP plumbing shared by every route.
package middleware

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"aurumtv/internal/auth/hmacsig"
	"aurumtv/internal/httpx"
	"aurumtv/internal/platform/clock"
)

type Middleware func(http.Handler) http.Handler

// Chain applies middleware so the first one listed runs first.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Recover turns a panic into a 500 without leaking details to the client.
func Recover(log *slog.Logger, production bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler {
						panic(v)
					}
					log.Error("panic", "path", r.URL.Path, "value", v, "stack", string(debug.Stack()))
					extra := map[string]any{}
					if !production {
						extra["detail"] = errors.New("panic").Error()
					}
					httpx.JSON(w, http.StatusInternalServerError, httpx.ErrorBody("internal_error", "internal server error", extra))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog logs method, path (no query: it may hold identifiers), status and duration.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
		})
	}
}

// SecurityHeaders sets hardened defaults. Logos/avatars are loaded cross-origin, so CORP is cross-origin.
func SecurityHeaders() Middleware {
	headers := map[string]string{
		"Content-Security-Policy":           "default-src 'none'; frame-ancestors 'none'; img-src 'self' data:; style-src 'unsafe-inline'",
		"Cross-Origin-Resource-Policy":      "cross-origin",
		"Cross-Origin-Opener-Policy":        "same-origin",
		"Origin-Agent-Cluster":              "?1",
		"Referrer-Policy":                   "no-referrer",
		"Strict-Transport-Security":         "max-age=15552000; includeSubDomains",
		"X-Content-Type-Options":            "nosniff",
		"X-Dns-Prefetch-Control":            "off",
		"X-Download-Options":                "noopen",
		"X-Frame-Options":                   "SAMEORIGIN",
		"X-Permitted-Cross-Domain-Policies": "none",
		"X-Xss-Protection":                  "0",
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CORS allows the configured origins ("*" by default) and the headers the app and admin tools use.
func CORS(origins []string) Middleware {
	allowAll := false
	allowed := map[string]bool{}
	for _, o := range origins {
		if o == "*" {
			allowAll = true
		}
		allowed[o] = true
	}
	allowHeaders := strings.Join([]string{"Content-Type", "Authorization", "X-Api-Key",
		hmacsig.HeaderKeyID, hmacsig.HeaderTimestamp, hmacsig.HeaderNonce, hmacsig.HeaderSignature}, ", ")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			switch {
			case allowAll:
				w.Header().Set("Access-Control-Allow-Origin", "*")
			case origin != "" && allowed[origin]:
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Expose-Headers", HeaderServerTime)
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Set("Access-Control-Allow-Methods", "GET,HEAD,PUT,POST,DELETE,PATCH")
				w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// HeaderServerTime carries the server clock on every response; the app uses it to correct a
// TV's wrong clock before signing requests.
const HeaderServerTime = "X-Tv-Server-Time"

func ServerTime(c clock.Clock) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(HeaderServerTime, strconv.FormatInt(clock.Unix(c), 10))
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP trusts X-Forwarded-For / X-Real-IP only behind a reverse proxy (trustProxy); otherwise
// a client could spoof its address to dodge the rate limit.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		// The proxy appends the peer it saw, so the rightmost entry is the only one a client
		// cannot forge (the leftmost is whatever the client sent).
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
				return last
			}
		}
		if real := strings.TrimSpace(r.Header.Get("X-Real-Ip")); real != "" {
			return real
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RateLimit is a per-IP fixed window (one minute). Enough for a single instance.
type RateLimit struct {
	limit      int
	trustProxy bool
	clock      clock.Clock
	mu         sync.Mutex
	buckets    map[string]*bucket
}

type bucket struct {
	window int64
	count  int
}

func NewRateLimit(perMinute int, trustProxy bool, c clock.Clock) *RateLimit {
	return &RateLimit{limit: perMinute, trustProxy: trustProxy, clock: c, buckets: map[string]*bucket{}}
}

func (rl *RateLimit) Middleware(next http.Handler) http.Handler {
	if rl.limit <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := rl.clock.Now()
		window := now.Unix() / 60
		ip := ClientIP(r, rl.trustProxy)
		rl.mu.Lock()
		b := rl.buckets[ip]
		if b == nil || b.window != window {
			b = &bucket{window: window}
			rl.buckets[ip] = b
		}
		b.count++
		over := b.count > rl.limit
		rl.mu.Unlock()
		if over {
			retry := (window+1)*60 - now.Unix()
			w.Header().Set("Retry-After", strconv.FormatInt(max(retry, 1), 10))
			httpx.Fail(w, http.StatusTooManyRequests, "rate_limited", "too many requests — try again shortly")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Sweep drops buckets older than two windows (call periodically).
func (rl *RateLimit) Sweep() {
	cutoff := rl.clock.Now().Unix()/60 - 2
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for ip, b := range rl.buckets {
		if b.window < cutoff {
			delete(rl.buckets, ip)
		}
	}
}

// BodyLimit buffers request bodies up to limit bytes (413 above it).
func BodyLimit(limit int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r2, err := httpx.BufferBody(r, limit)
			if err != nil {
				if errors.Is(err, httpx.ErrBodyTooLarge) {
					httpx.Fail(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large")
					return
				}
				httpx.Fail(w, http.StatusBadRequest, "invalid_body", "could not read the request body")
				return
			}
			next.ServeHTTP(w, r2)
		})
	}
}
