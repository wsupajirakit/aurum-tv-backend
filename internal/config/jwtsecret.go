package config

import (
	"aurumtv/internal/platform/fsx"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const secretBytes = 48

// loadOrCreateSecret returns a secret stored in dataDir, creating it (mode 0600) on first use.
// Used only when the secret is not provided through the environment: nothing is hardcoded,
// and the value survives restarts and redeploys because it lives on the data volume.
func loadOrCreateSecret(dataDir, name string) (value string, created bool, err error) {
	file := filepath.Join(dataDir, ".secret-"+strings.ToLower(name))
	if b, err := os.ReadFile(file); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v, false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err := os.MkdirAll(dataDir, fsx.DirMode); err != nil {
		return "", false, err
	}
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	value = base64.RawURLEncoding.EncodeToString(buf)
	if err := os.WriteFile(file, []byte(value), fsx.FileMode); err != nil {
		return "", false, err
	}
	return value, true, nil
}
