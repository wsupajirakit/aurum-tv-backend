// Package apitest boots the complete API in-process for tests: real configuration, real services,
// a temporary DATA_DIR and a fake stream probe (no network).
package apitest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"aurumtv/internal/api"
	"aurumtv/internal/auth"
	"aurumtv/internal/auth/appauth"
	"aurumtv/internal/auth/hmacsig"
	"aurumtv/internal/channel"
	"aurumtv/internal/config"
	"aurumtv/internal/hls"
	"aurumtv/internal/logs"
	"aurumtv/internal/middleware"
	"aurumtv/internal/platform/clock"
	"aurumtv/internal/session"
	"aurumtv/internal/source"
	"aurumtv/internal/source/resolver"
)

const (
	KeyID     = "tv1"
	Secret    = "test-hmac-secret-0123456789abcdef"
	APIKey    = "test-admin-key"
	DynamicID = "dyn-ch3-0001"
)

// FixtureV1 is pre-sources data — booting on it also exercises the migration.
const FixtureV1 = `{"version":1,"updatedAt":"2026-01-01T00:00:00.000Z","channels":[
 {"id":"static-ch-0001","name":"One 31","url":"https://example.com/one/playlist.m3u8","group":"ฟรีทีวี","order":0},
 {"id":"static-ch-0002","name":"Workpoint 23","url":"https://example.com/wp/playlist.m3u8","group":"ฟรีทีวี","order":1},
 {"id":"static-ch-0003","name":"GMM 25","url":"https://example.com/gmm/playlist.m3u8","group":"ฟรีทีวี","order":2},
 {"id":"dyn-ch3-0001","name":"3HD","url":"https://ch3-33-web.cdn.byteark.com/live/playlist.m3u8","group":"ฟรีทีวี","order":3}]}`

// Env is a booted API.
type Env struct {
	Handler  http.Handler
	DataDir  string
	Channels *channel.Repository
	Monitor  *source.Monitor
	Sessions *session.Service
	Logs     *logs.Service
}

// ProbeTable maps a url to whether it plays.
type ProbeTable map[string]bool

// FakeProbe answers from a table instead of the network and records the probed urls (the monitor
// probes concurrently, so recording is serialized).
func FakeProbe(table ProbeTable, calls *[]string) source.ProbeFunc {
	var mu sync.Mutex
	return func(_ context.Context, s channel.Source, _ bool) source.Outcome {
		if calls != nil {
			mu.Lock()
			*calls = append(*calls, s.URL)
			mu.Unlock()
		}
		r := hls.Result{OK: table[s.URL], Stage: "segment", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
		if !r.OK {
			reason := "http_404"
			r.Stage, r.Reason = "playlist", &reason
		}
		return source.Outcome{Result: r, ResolvedURL: s.URL}
	}
}

// Boot starts the API on FixtureV1 (or the seed when fixture is ""); vars override the defaults.
func Boot(t testing.TB, fixture string, vars map[string]string) *Env {
	t.Helper()
	dir := t.TempDir()
	if fixture != "" {
		if err := os.WriteFile(filepath.Join(dir, "channels.json"), []byte(fixture), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := map[string]string{
		"DATA_DIR": dir, "API_KEY": APIKey, "JWT_SECRET": "test-jwt-secret-0123456789abcdef0123456789",
		"HMAC_KEYS": KeyID + ":" + Secret, "RATE_LIMIT_PER_MIN": "0", "HEALTH_ENABLED": "false",
		"HEALTH_CONFIRM_DELAY_MS": "1", "LEGACY_DYNAMIC_CHANNEL_IDS": DynamicID,
	}
	for k, v := range vars {
		base[k] = v
	}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := base[k]; return v, ok }, dir)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	clk := clock.System{}
	channels := channel.NewRepository(cfg.Paths, cfg.Stream.LegacyDynamicIDs, clk, log)
	if err := channels.Load(); err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewTokens(cfg.JWTSecret, clk, cfg.Session.AccessTTL, cfg.AdminTokenTTL)
	sessions := session.NewService(session.Options{Store: session.NewStore(cfg.Paths.SessionsFile, log), Tokens: tokens, Clock: clk,
		Secret: cfg.JWTSecret, AccessTTL: cfg.Session.AccessTTL, RefreshTTL: cfg.Session.RefreshTTL, Grace: cfg.Session.RefreshGrace, Log: log})
	keys := auth.NewAPIKeys(cfg.APIKeys)
	guard := appauth.New(appauth.Options{Mode: cfg.HMAC.Mode, RequireRead: cfg.HMAC.RequireRead,
		Verifier: hmacsig.NewVerifier(cfg.HMAC.Keys, cfg.HMAC.MaxSkew, clk), Tokens: tokens, Sessions: sessions,
		APIKeys: keys, Clock: clk, Log: log})
	sourceURL := base["CH3_SOURCE_URL"]
	if sourceURL == "" {
		sourceURL = "http://127.0.0.1:1/unreachable"
	}
	registry := resolver.New(resolver.Ch3Plus{Client: http.DefaultClient, SourceURL: sourceURL,
		Timeout: time.Second, RenewBefore: cfg.Stream.RenewBefore, Clock: clk})
	health := source.NewStore(cfg.Paths.SourceHealthFile, log)
	monitor := source.NewMonitor(channels, health, FakeProbe(ProbeTable{}, nil), source.MonitorOptions{
		Concurrency: 2, ConfirmDelay: time.Millisecond, ReportCooldown: time.Minute, Interval: time.Hour,
		Thresholds: source.Thresholds{Fail: 2, Recover: 2}, Schedule: source.Schedule{BackupEveryCycles: 3, DownBackoffMaxCycles: 8},
		Policy: source.Policy{PreferPrimary: true, RecoverThreshold: 2},
	}, log)
	logService := logs.NewService(cfg.Paths.LogsDir, cfg.Logs.Enabled, cfg.Logs.MaxBatch, clk, log)
	t.Cleanup(logService.Flush)
	h := api.New(api.Deps{Config: cfg, Clock: clk, Log: log, Channels: channels, Sessions: sessions, Tokens: tokens,
		APIKeys: keys, Guard: guard, Health: health, Monitor: monitor, Refresher: source.NewRefresher(channels, registry, log),
		Resolvers: registry, Logs: logService, RateLimit: middleware.NewRateLimit(cfg.RateLimitPerMin, cfg.TrustProxy, clk),
		HTTPClient: http.DefaultClient})
	return &Env{Handler: h, DataDir: dir, Channels: channels, Monitor: monitor, Sessions: sessions, Logs: logService}
}
