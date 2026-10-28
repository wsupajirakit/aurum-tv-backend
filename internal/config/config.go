// Package config loads every tunable setting from the environment.
// Names and defaults match the documented .env.example; nothing secret has a default in code.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aurumtv/internal/stream"
)

// HMAC modes.
const (
	HMACOff     = "off"
	HMACLog     = "log"
	HMACEnforce = "enforce"
)

type HMAC struct {
	// Mode is enforce (default), log (warn only, debugging) or off.
	Mode        string
	Keys        map[string]string
	MaxSkew     time.Duration
	RequireRead bool
}

type Session struct {
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	// RefreshGrace keeps the previous refresh token valid briefly, in case a response was lost.
	RefreshGrace time.Duration
}

type Stream struct {
	SourceURL        string
	FetchTimeout     time.Duration
	RenewBefore      time.Duration
	RefreshEvery     time.Duration
	SyncAllowedHosts []string
	LegacyDynamicIDs []string
}

type Probe struct {
	Timeout          time.Duration
	MaxPlaylistBytes int64
	SegmentBytes     int64
	Variant          string // lowest | highest
}

type Health struct {
	Enabled              bool
	Interval             time.Duration
	Concurrency          int
	BackupEveryCycles    int
	DownBackoffMaxCycles int
	FailThreshold        int
	RecoverThreshold     int
	ConfirmDelay         time.Duration
	PreferPrimary        bool
	ReportCooldown       time.Duration
	Probe                Probe
}

type Logs struct {
	Enabled   bool
	MaxBatch  int
	BodyLimit int64
}

type Paths struct {
	DataDir          string
	ChannelsFile     string
	BackupFile       string
	TmpFile          string
	SessionsFile     string
	SourceHealthFile string
	LogsDir          string
}

type Config struct {
	Production        bool
	Port              int
	APIKeys           []string
	PublicRead        bool
	RateLimitPerMin   int
	TrustProxy        bool
	CORSOrigins       []string
	EnableDiagnostics bool
	BodyLimit         int64
	JWTSecret         string
	AdminTokenTTL     time.Duration

	HMAC    HMAC
	Session Session
	Stream  Stream
	Health  Health
	Logs    Logs
	Paths   Paths

	// Warnings are printed once at startup.
	Warnings []string
}

// Default Channel 3 ids of the legacy (v1) data, used only by the one-time data migration.
var defaultLegacyDynamicIDs = []string{
	"834500c3-7a3e-4d58-a7df-6d0c981fb111",
	"d100002d-bc97-4852-abfa-f80224804a8f",
}

// Load reads the configuration. lookup is os.LookupEnv in production and a map in tests.
func Load(lookup func(string) (string, bool), workDir string) (*Config, error) {
	e := env{lookup: lookup}
	appEnv := e.str("APP_ENV", e.str("NODE_ENV", "development"))
	c := &Config{
		Production:        appEnv == "production",
		Port:              e.num("PORT", 3000),
		APIKeys:           e.list("API_KEY", nil),
		PublicRead:        e.boolean("PUBLIC_READ", true),
		RateLimitPerMin:   e.num("RATE_LIMIT_PER_MIN", 120),
		TrustProxy:        e.boolean("TRUST_PROXY", true),
		CORSOrigins:       e.list("CORS_ORIGINS", []string{"*"}),
		EnableDiagnostics: e.boolean("ENABLE_DIAGNOSTICS", false),
		BodyLimit:         int64(e.num("BODY_LIMIT_BYTES", 1024*1024)),
		AdminTokenTTL:     seconds(e.num("ADMIN_TOKEN_TTL_SEC", 7*86400)),
		HMAC: HMAC{
			Mode:        hmacMode(e.str("HMAC_MODE", HMACEnforce)),
			Keys:        parseHMACKeys(e.list("HMAC_KEYS", nil)),
			MaxSkew:     seconds(e.num("HMAC_MAX_SKEW_SEC", 300)),
			RequireRead: e.boolean("HMAC_REQUIRE_READ", true),
		},
		Session: Session{
			AccessTTL:    seconds(e.num("ACCESS_TOKEN_TTL_SEC", 15*60)),
			RefreshTTL:   seconds(e.num("REFRESH_TOKEN_TTL_SEC", 30*86400)),
			RefreshGrace: seconds(e.num("REFRESH_GRACE_SEC", 60)),
		},
		Stream: Stream{
			SourceURL:        e.str("CH3_SOURCE_URL", stream.DefaultSourceURL),
			FetchTimeout:     millis(e.num("STREAM_FETCH_TIMEOUT_MS", 10_000)),
			RenewBefore:      seconds(e.num("STREAM_RENEW_BEFORE_SEC", 2*3600)),
			RefreshEvery:     millis(e.num("STREAM_REFRESH_EVERY_MS", 30*60_000)),
			SyncAllowedHosts: e.list("SYNC_ALLOWED_HOSTS", []string{"byteark.com"}),
			LegacyDynamicIDs: e.list("LEGACY_DYNAMIC_CHANNEL_IDS", e.list("DYNAMIC_CHANNEL_IDS", defaultLegacyDynamicIDs)),
		},
		Health: Health{
			Enabled:              e.boolean("HEALTH_ENABLED", true),
			Interval:             millis(e.num("HEALTH_INTERVAL_MS", 5*60_000)),
			Concurrency:          max(1, e.num("HEALTH_CONCURRENCY", 4)),
			BackupEveryCycles:    max(1, e.num("HEALTH_BACKUP_EVERY_CYCLES", 3)),
			DownBackoffMaxCycles: max(1, e.num("HEALTH_DOWN_BACKOFF_MAX_CYCLES", 8)),
			FailThreshold:        max(1, e.num("HEALTH_FAIL_THRESHOLD", 2)),
			RecoverThreshold:     max(1, e.num("HEALTH_RECOVER_THRESHOLD", 2)),
			ConfirmDelay:         millis(e.num("HEALTH_CONFIRM_DELAY_MS", 3000)),
			PreferPrimary:        e.boolean("HEALTH_PREFER_PRIMARY", true),
			ReportCooldown:       millis(e.num("HEALTH_REPORT_COOLDOWN_MS", 30_000)),
			Probe: Probe{
				Timeout:          millis(e.num("HEALTH_PROBE_TIMEOUT_MS", 10_000)),
				MaxPlaylistBytes: int64(e.num("HEALTH_MAX_PLAYLIST_BYTES", 2*1024*1024)),
				SegmentBytes:     int64(e.num("HEALTH_SEGMENT_BYTES", 4096)),
				Variant:          e.str("HEALTH_PROBE_VARIANT", "lowest"),
			},
		},
		Logs: Logs{
			Enabled:   e.boolean("ENABLE_LOGS", true),
			MaxBatch:  e.num("LOG_MAX_BATCH", 200),
			BodyLimit: int64(e.num("LOG_BODY_LIMIT_BYTES", 512*1024)),
		},
	}

	dataDir := e.str("DATA_DIR", "data")
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(workDir, dataDir)
	}
	c.Paths = Paths{
		DataDir:          dataDir,
		ChannelsFile:     filepath.Join(dataDir, "channels.json"),
		BackupFile:       filepath.Join(dataDir, "channels.bak.json"),
		TmpFile:          filepath.Join(dataDir, "channels.tmp.json"),
		SessionsFile:     filepath.Join(dataDir, "sessions.json"),
		SourceHealthFile: filepath.Join(dataDir, "source-health.json"),
		LogsDir:          filepath.Join(dataDir, "logs"),
	}

	// Production with app authentication on but no keys would lock every TV out: refuse to start.
	if c.Production && c.HMAC.Mode != HMACOff && len(c.HMAC.Keys) == 0 {
		return nil, fmt.Errorf("config: HMAC_MODE=%s but HMAC_KEYS is empty (format kid:secret) — set it before deploying", c.HMAC.Mode)
	}

	secret := e.str("JWT_SECRET", "")
	if secret == "" {
		value, created, err := loadOrCreateSecret(dataDir, "JWT_SECRET")
		if err != nil {
			return nil, fmt.Errorf("config: JWT_SECRET: %w", err)
		}
		secret = value
		if created {
			c.Warnings = append(c.Warnings, "JWT_SECRET is not set — generated one and stored it in DATA_DIR (set it in the environment)")
		} else {
			c.Warnings = append(c.Warnings, "JWT_SECRET is not set — using the value stored in DATA_DIR")
		}
	}
	c.JWTSecret = secret

	if len(c.APIKeys) == 0 {
		c.Warnings = append(c.Warnings, "API_KEY is not set — admin endpoints are disabled")
	}
	if c.HMAC.Mode != HMACOff && len(c.HMAC.Keys) == 0 {
		c.Warnings = append(c.Warnings, "HMAC_KEYS is empty — signed app requests cannot pass")
	}
	return c, nil
}

// FromOS loads .env from the working directory, then the process environment.
func FromOS() (*Config, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if err := LoadDotEnv(filepath.Join(wd, ".env")); err != nil {
		return nil, errors.Join(errors.New("config: reading .env"), err)
	}
	return Load(os.LookupEnv, wd)
}

func hmacMode(v string) string {
	switch m := strings.ToLower(v); m {
	case HMACOff, HMACLog, HMACEnforce:
		return m
	default:
		return HMACEnforce
	}
}

// parseHMACKeys turns "kid:secret,kid2:secret2" into a map; malformed pairs are ignored.
func parseHMACKeys(pairs []string) map[string]string {
	keys := make(map[string]string, len(pairs))
	for _, p := range pairs {
		kid, secret, ok := strings.Cut(p, ":")
		kid, secret = strings.TrimSpace(kid), strings.TrimSpace(secret)
		if ok && kid != "" && secret != "" {
			keys[kid] = secret
		}
	}
	return keys
}

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }
func millis(n int) time.Duration  { return time.Duration(n) * time.Millisecond }
