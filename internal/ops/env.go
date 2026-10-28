// Package ops holds the operator tools run by aurumctl. They talk to a deployed backend with the
// admin key (X-API-Key) — admin endpoints do not depend on HMAC_MODE, so they keep working under
// enforce — or, for smoke, exactly like the TV app (HMAC-signed requests plus a session).
package ops

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"aurumtv/internal/config"
)

// DefaultEnvFile is read from the working directory unless AURUMCTL_ENV points elsewhere.
// It is separate from the server's .env and is never committed.
const DefaultEnvFile = "ops.env"

// ErrConfig marks a missing or invalid setting (exit code 2).
var ErrConfig = errors.New("configuration")

// LoadEnv loads the ops env file; variables already set by the shell or cron always win.
func LoadEnv() error {
	file := os.Getenv("AURUMCTL_ENV")
	if file == "" {
		file = DefaultEnvFile
	}
	return config.LoadDotEnv(file)
}

func required(name, hint string) (string, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return "", fmt.Errorf("%w: %s is not set (%s) — put it in %s or the shell environment", ErrConfig, name, hint, DefaultEnvFile)
	}
	return v, nil
}

func optional(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func millis(name string, fallback int) (time.Duration, error) {
	n, err := strconv.Atoi(optional(name, strconv.Itoa(fallback)))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %s must be a positive number of milliseconds", ErrConfig, name)
	}
	return time.Duration(n) * time.Millisecond, nil
}

// AdminFromEnv builds the admin client from TV_API_URL / TV_ADMIN_API_KEY / OPS_TIMEOUT_MS.
func AdminFromEnv(defaultTimeoutMs int) (*Admin, error) {
	base, err := required("TV_API_URL", "the backend URL, e.g. https://tv.example.com")
	if err != nil {
		return nil, err
	}
	key, err := required("TV_ADMIN_API_KEY", "the same value as the backend API_KEY")
	if err != nil {
		return nil, err
	}
	timeout, err := millis("OPS_TIMEOUT_MS", defaultTimeoutMs)
	if err != nil {
		return nil, err
	}
	return NewAdmin(base, key, timeout)
}
