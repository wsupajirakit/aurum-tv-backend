package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// LoadDotEnv reads KEY=VALUE lines from path into the process environment.
// Variables that are already set (by the shell, Docker or the hosting platform) always win.
// A missing file is not an error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if _, exists := os.LookupEnv(key); exists || key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.TrimPrefix(strings.TrimSuffix(value, `"`), `"`)
		value = strings.TrimPrefix(strings.TrimSuffix(value, `'`), `'`)
		_ = os.Setenv(key, value)
	}
	return scanner.Err()
}

// env reads typed values; an unset or blank variable yields the fallback.
type env struct{ lookup func(string) (string, bool) }

func (e env) str(name, fallback string) string {
	if v, ok := e.lookup(name); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func (e env) num(name string, fallback int) int {
	v := e.str(name, "")
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return int(f)
}

func (e env) boolean(name string, fallback bool) bool {
	v := e.str(name, "")
	if v == "" {
		return fallback
	}
	return !strings.EqualFold(v, "false")
}

func (e env) list(name string, fallback []string) []string {
	v := e.str(name, "")
	if v == "" {
		return fallback
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
