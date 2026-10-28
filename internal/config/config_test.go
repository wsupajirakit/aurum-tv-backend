package config

import (
	"strings"
	"testing"
)

func lookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDefaultsAreSecure(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(lookup(map[string]string{"DATA_DIR": dir}), dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.HMAC.Mode != HMACEnforce || !c.HMAC.RequireRead || !c.PublicRead || c.EnableDiagnostics {
		t.Fatalf("unsafe defaults: %+v", c.HMAC)
	}
	if c.JWTSecret == "" {
		t.Fatal("a JWT secret must be generated")
	}
	again, _ := Load(lookup(map[string]string{"DATA_DIR": dir}), dir)
	if again.JWTSecret != c.JWTSecret {
		t.Fatal("the generated secret must persist")
	}
}

func TestProductionRefusesToStartWithoutHMACKeys(t *testing.T) {
	_, err := Load(lookup(map[string]string{"APP_ENV": "production", "DATA_DIR": t.TempDir()}), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "HMAC_KEYS") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if _, err := Load(lookup(map[string]string{"APP_ENV": "production", "HMAC_KEYS": "tv1:s", "DATA_DIR": t.TempDir()}), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestParsing(t *testing.T) {
	keys := parseHMACKeys([]string{"tv1:a", "bad", ":x", "tv2: b "})
	if len(keys) != 2 || keys["tv2"] != "b" {
		t.Fatalf("keys %v", keys)
	}
	if hmacMode("LOG") != HMACLog || hmacMode("weird") != HMACEnforce {
		t.Fatal("mode parsing")
	}
}
