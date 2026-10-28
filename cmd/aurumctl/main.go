// Command aurumctl holds the operator tools for a deployed Aurum TV backend.
//
//	aurumctl sources-status   [--probe <channelId>]
//	aurumctl discover-sources [--apply] [--channel <id>] [--rules <file>]
//	aurumctl sync-ch3         [--dry-run] [--force] [--loop <minutes>]
//	aurumctl restore-channels [--dry-run]
//	aurumctl smoke            (SMOKE_URL, SMOKE_KEY_ID, SMOKE_SECRET, SMOKE_REAL_SOURCE)
//	aurumctl genkey           [--key-id <id>]
//
// Settings come from ops.env in the working directory (or the file named by AURUMCTL_ENV) and the
// shell environment. Exit codes: 0 success · 1 failure · 2 missing or invalid configuration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"aurumtv/internal/hls"
	"aurumtv/internal/ops"
	"aurumtv/internal/stream"
)

const (
	exitOK     = 0
	exitFailed = 1
	exitConfig = 2
)

type command struct {
	summary string
	run     func(ctx context.Context, args []string) error
}

var commands = map[string]command{
	"sources-status":   {"health of every channel's sources", sourcesStatus},
	"discover-sources": {"find backup sources and verify they play", discoverSources},
	"sync-ch3":         {"push a fresh ch3plus link to the backend", syncCh3},
	"restore-channels": {"re-add seed channels missing on the backend", restoreChannels},
	"smoke":            {"end-to-end check against a running server, signed like the TV app", smoke},
	"genkey":           {"print fresh HMAC and admin keys for .env", genkey},
}

var order = []string{"sources-status", "discover-sources", "sync-ch3", "restore-channels", "smoke", "genkey"}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(exitConfig)
	}
	name := os.Args[1]
	cmd, ok := commands[name]
	if !ok {
		if name == "help" || name == "-h" || name == "--help" {
			usage(os.Stdout)
			os.Exit(exitOK)
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", name)
		usage(os.Stderr)
		os.Exit(exitConfig)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := ops.LoadEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitConfig)
	}
	err := cmd.run(ctx, os.Args[2:])
	switch {
	case err == nil:
		os.Exit(exitOK)
	case errors.Is(err, ops.ErrConfig), errors.Is(err, flag.ErrHelp):
		if !errors.Is(err, flag.ErrHelp) {
			logger(name).Error("%v", err)
		}
		os.Exit(exitConfig)
	default:
		logger(name).Error("%v", err)
		os.Exit(exitFailed)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: aurumctl <command> [flags]\n\ncommands:")
	for _, n := range order {
		fmt.Fprintf(w, "  %-18s %s\n", n, commands[n].summary)
	}
	fmt.Fprintln(w, "\nrun `aurumctl <command> -h` for its flags; settings are read from ops.env")
}

func logger(tag string) ops.Logger { return ops.Logger{Out: os.Stdout, Err: os.Stderr, Tag: tag} }

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: unexpected argument %q", ops.ErrConfig, fs.Arg(0))
	}
	return nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func positiveInt(name, fallback string) (int, error) {
	n, err := strconv.Atoi(envOr(name, fallback))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %s must be a positive number", ops.ErrConfig, name)
	}
	return n, nil
}

func sourcesStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sources-status", flag.ContinueOnError)
	probe := fs.String("probe", "", "probe this channel id now, then show")
	if err := parse(fs, args); err != nil {
		return err
	}
	admin, err := ops.AdminFromEnv(60000)
	if err != nil {
		return err
	}
	return ops.SourcesStatus(ctx, admin, *probe, os.Stdout)
}

func discoverSources(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("discover-sources", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "append the verified sources (never removes or reorders)")
	channelID := fs.String("channel", "", "only this channel id")
	rulesFile := fs.String("rules", "", "rules file (default: built-in rules)")
	if err := parse(fs, args); err != nil {
		return err
	}
	admin, err := ops.AdminFromEnv(15000)
	if err != nil {
		return err
	}
	rules, err := ops.LoadRules(*rulesFile)
	if err != nil {
		return err
	}
	timeoutMs, err := positiveInt("OPS_PROBE_TIMEOUT_MS", "10000")
	if err != nil {
		return err
	}
	concurrency, err := positiveInt("OPS_PROBE_CONCURRENCY", "4")
	if err != nil {
		return err
	}
	probe := hls.Options{Timeout: time.Duration(timeoutMs) * time.Millisecond, MaxPlaylistBytes: 2 << 20, SegmentBytes: 4096,
		Headers: stream.BrowserHeaders, Client: &http.Client{}}
	return ops.Discover(ctx, admin, ops.DiscoverOptions{Apply: *apply, ChannelID: *channelID, Rules: rules, Concurrency: concurrency,
		Probe: func(ctx context.Context, u string) hls.Result { return hls.Probe(ctx, u, probe) }}, logger("discover"))
}

func syncCh3(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync-ch3", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "report only")
	force := fs.Bool("force", false, "update even when the link is unchanged")
	loop := fs.Int("loop", 0, "keep running, once every N minutes (an alternative to cron)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *loop < 0 {
		return fmt.Errorf("%w: --loop must be positive", ops.ErrConfig)
	}
	admin, err := ops.AdminFromEnv(15000)
	if err != nil {
		return err
	}
	timeoutMs, err := positiveInt("OPS_TIMEOUT_MS", "15000")
	if err != nil {
		return err
	}
	o := ops.SyncOptions{SourceURL: envOr("CH3_SOURCE_URL", stream.DefaultSourceURL), Timeout: time.Duration(timeoutMs) * time.Millisecond,
		DryRun: *dryRun, Force: *force, Client: &http.Client{}}
	log := logger("sync-ch3")
	if *loop == 0 {
		_, err := ops.SyncCh3(ctx, admin, o, log)
		return err
	}
	log.Info("running every %d minute(s) — Ctrl+C to stop", *loop)
	ticker := time.NewTicker(time.Duration(*loop) * time.Minute)
	defer ticker.Stop()
	for {
		if _, err := ops.SyncCh3(ctx, admin, o, log); err != nil {
			log.Error("%v", err) // a failed run is skipped; the next one tries again
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func restoreChannels(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("restore-channels", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "report only")
	if err := parse(fs, args); err != nil {
		return err
	}
	admin, err := ops.AdminFromEnv(15000)
	if err != nil {
		return err
	}
	return ops.RestoreChannels(ctx, admin, *dryRun, logger("restore"))
}

func smoke(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	if err := parse(fs, args); err != nil {
		return err
	}
	o := ops.SmokeOptions{BaseURL: envOr("SMOKE_URL", "http://localhost:3000"), KeyID: os.Getenv("SMOKE_KEY_ID"),
		Secret: os.Getenv("SMOKE_SECRET"), RealSource: os.Getenv("SMOKE_REAL_SOURCE") == "true", Client: &http.Client{Timeout: 30 * time.Second}}
	if failures := ops.Smoke(ctx, o, os.Stdout); failures > 0 {
		return fmt.Errorf("%d smoke check(s) failed", failures)
	}
	return nil
}

func genkey(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("genkey", flag.ContinueOnError)
	keyID := fs.String("key-id", "tv1", "HMAC key id")
	if err := parse(fs, args); err != nil {
		return err
	}
	return ops.GenKey(*keyID, os.Stdout)
}
