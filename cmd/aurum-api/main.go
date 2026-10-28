// Command aurum-api serves the Aurum TV API.
//
//	aurum-api               run the server (configuration from the environment / .env)
//	aurum-api healthcheck   exit 0 when GET /api/health answers 200 (used by the container)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
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
	shutdownGrace     = 5 * time.Second
	sessionSweepEvery = time.Hour
	rateSweepEvery    = 5 * time.Minute
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func healthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Get("http://127.0.0.1:" + port + "/api/health")
	if err != nil {
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func newLogger(production bool) *slog.Logger {
	if production {
		return slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, nil))
}

func run() error {
	cfg, err := config.FromOS()
	if err != nil {
		return err
	}
	log := newLogger(cfg.Production)
	for _, w := range cfg.Warnings {
		log.Warn(w)
	}
	clk := clock.System{}
	outbound := &http.Client{Timeout: 30 * time.Second}

	channels := channel.NewRepository(cfg.Paths, cfg.Stream.LegacyDynamicIDs, clk, log)
	if err := channels.Load(); err != nil {
		return fmt.Errorf("loading channels: %w", err)
	}

	tokens := auth.NewTokens(cfg.JWTSecret, clk, cfg.Session.AccessTTL, cfg.AdminTokenTTL)
	sessionStore := session.NewStore(cfg.Paths.SessionsFile, log)
	if err := sessionStore.Load(clock.Unix(clk)); err != nil {
		return err
	}
	sessions := session.NewService(session.Options{Store: sessionStore, Tokens: tokens, Clock: clk, Secret: cfg.JWTSecret,
		AccessTTL: cfg.Session.AccessTTL, RefreshTTL: cfg.Session.RefreshTTL, Grace: cfg.Session.RefreshGrace, Log: log})
	apiKeys := auth.NewAPIKeys(cfg.APIKeys)
	guard := appauth.New(appauth.Options{Mode: cfg.HMAC.Mode, RequireRead: cfg.HMAC.RequireRead,
		Verifier: hmacsig.NewVerifier(cfg.HMAC.Keys, cfg.HMAC.MaxSkew, clk), Tokens: tokens, Sessions: sessions,
		APIKeys: apiKeys, Clock: clk, Log: log})

	registry := resolver.New(resolver.Ch3Plus{Client: outbound, SourceURL: cfg.Stream.SourceURL,
		Timeout: cfg.Stream.FetchTimeout, RenewBefore: cfg.Stream.RenewBefore, Clock: clk})
	refresher := source.NewRefresher(channels, registry, log)
	healthStore := source.NewStore(cfg.Paths.SourceHealthFile, log)
	if err := healthStore.Load(); err != nil {
		return err
	}
	probeOpts := hls.Options{Timeout: cfg.Health.Probe.Timeout, MaxPlaylistBytes: cfg.Health.Probe.MaxPlaylistBytes,
		SegmentBytes: cfg.Health.Probe.SegmentBytes, Variant: cfg.Health.Probe.Variant, Client: outbound}
	monitor := source.NewMonitor(channels, healthStore, source.NewProber(registry, probeOpts), source.MonitorOptions{
		Interval: cfg.Health.Interval, Concurrency: cfg.Health.Concurrency, ConfirmDelay: cfg.Health.ConfirmDelay,
		ReportCooldown: cfg.Health.ReportCooldown,
		Thresholds:     source.Thresholds{Fail: cfg.Health.FailThreshold, Recover: cfg.Health.RecoverThreshold},
		Schedule:       source.Schedule{BackupEveryCycles: cfg.Health.BackupEveryCycles, DownBackoffMaxCycles: cfg.Health.DownBackoffMaxCycles},
		Policy:         source.Policy{PreferPrimary: cfg.Health.PreferPrimary, RecoverThreshold: cfg.Health.RecoverThreshold},
	}, log)
	logService := logs.NewService(cfg.Paths.LogsDir, cfg.Logs.Enabled, cfg.Logs.MaxBatch, clk, log)
	rateLimit := middleware.NewRateLimit(cfg.RateLimitPerMin, cfg.TrustProxy, clk)

	handler := api.New(api.Deps{Config: cfg, Clock: clk, Log: log, Channels: channels, Sessions: sessions, Tokens: tokens,
		APIKeys: apiKeys, Guard: guard, Health: healthStore, Monitor: monitor, Refresher: refresher, Resolvers: registry,
		Logs: logService, RateLimit: rateLimit, HTTPClient: outbound})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Background work: renew resolver links, self-healing, housekeeping.
	go refresher.EnsureFresh(ctx)
	go refresher.Run(ctx, cfg.Stream.RefreshEvery)
	if cfg.Health.Enabled {
		monitor.Start()
	}
	go every(ctx, sessionSweepEvery, func() { sessionStore.Sweep(clock.Unix(clk)) })
	go every(ctx, rateSweepEvery, rateLimit.Sweep)

	srv := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // SSE lifts this per connection
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info(api.ServiceName+" listening", "port", cfg.Port, "hmacMode", cfg.HMAC.Mode, "hmacKeys", len(cfg.HMAC.Keys),
			"selfHealing", cfg.Health.Enabled, "dataDir", "DATA_DIR")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down — flushing pending writes")
	monitor.Stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	logService.Flush()
	return errors.Join(sessionStore.Flush(), healthStore.Flush())
}

func every(ctx context.Context, d time.Duration, fn func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			fn()
		case <-ctx.Done():
			return
		}
	}
}
