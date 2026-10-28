package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestSecurityHeaders(t *testing.T) {
	rec := serve(SecurityHeaders()(ok), httptest.NewRequest("GET", "/", nil))
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
}

func TestCORSOnlyEchoesAllowedOrigins(t *testing.T) {
	h := CORS([]string{"https://admin.example"})(ok)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	if got := serve(h, req).Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin allowed: %q", got)
	}
	pre := httptest.NewRequest("OPTIONS", "/", nil)
	pre.Header.Set("Origin", "https://admin.example")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	rec := serve(h, pre)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://admin.example" ||
		!strings.Contains(strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers")), "x-tv-signature") {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}
}

func TestClientIPCannotBeSpoofed(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.2:5555"
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if ip := ClientIP(req, false); ip != "10.0.0.2" {
		t.Fatalf("headers must be ignored without TRUST_PROXY: %s", ip)
	}
	if ip := ClientIP(req, true); ip != "203.0.113.9" {
		t.Fatalf("the proxy-appended address must win: %s", ip)
	}
}

func TestRateLimitPerIPAndWindow(t *testing.T) {
	clk := &fixedClock{t: time.Unix(1_800_000_000, 0)}
	rl := NewRateLimit(2, false, clk)
	h := rl.Middleware(ok)
	call := func(addr string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = addr + ":1"
		return serve(h, r)
	}
	call("1.1.1.1")
	call("1.1.1.1")
	over := call("1.1.1.1")
	if over.Code != http.StatusTooManyRequests || over.Header().Get("Retry-After") == "" {
		t.Fatalf("want 429 with Retry-After, got %d", over.Code)
	}
	if call("2.2.2.2").Code != http.StatusOK {
		t.Fatal("other clients are unaffected")
	}
	clk.t = clk.t.Add(time.Minute)
	if call("1.1.1.1").Code != http.StatusOK {
		t.Fatal("new window resets the count")
	}
	rl.Sweep()
}

func TestRecoverHidesPanicsInProduction(t *testing.T) {
	boom := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret detail") })
	rec := serve(Recover(slog.New(slog.NewTextHandler(io.Discard, nil)), true)(boom), httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret detail") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestBodyLimit(t *testing.T) {
	h := BodyLimit(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		}
	}))
	if rec := serve(h, httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 64)))); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("%d", rec.Code)
	}
}
