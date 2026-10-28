package session

import (
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"aurumtv/internal/auth"
	"aurumtv/internal/httpx"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

const secret = "session-test-secret-0123456789abcdef0123"

func newService(t *testing.T, file string, clk *fakeClock) (*Service, *Store) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewStore(file, log)
	if err := store.Load(clk.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewTokens(secret, clk, 15*time.Minute, 7*24*time.Hour)
	return NewService(Options{Store: store, Tokens: tokens, Clock: clk, Secret: secret, AccessTTL: 15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour, Grace: time.Minute, Log: log}), store
}

func errCode(err error) string {
	if he, ok := err.(*httpx.Error); ok {
		return he.Code
	}
	return ""
}

func TestRotationGraceAndReuse(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	svc, _ := newService(t, filepath.Join(t.TempDir(), "sessions.json"), clk)

	first, err := svc.Start("tv-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Refresh(first.RefreshToken)
	if err != nil || second.RefreshToken == first.RefreshToken {
		t.Fatalf("rotation: %v", err)
	}
	// Lost response: the previous token still works inside the grace window...
	clk.Advance(30 * time.Second)
	if _, err := svc.Refresh(first.RefreshToken); err != nil {
		t.Fatalf("grace: %v", err)
	}
	// ...but the grace window is not extended by using it, and reuse afterwards revokes the session.
	clk.Advance(31 * time.Second)
	_, err = svc.Refresh(first.RefreshToken)
	if errCode(err) != "refresh_reused" {
		t.Fatalf("want refresh_reused, got %v", err)
	}
	if _, err := svc.Refresh(second.RefreshToken); errCode(err) != "refresh_invalid" {
		t.Fatalf("revoked session must stay revoked, got %v", err)
	}
}

func TestExpiryAndUnknownTokens(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	svc, _ := newService(t, filepath.Join(t.TempDir(), "sessions.json"), clk)
	if _, err := svc.Refresh("never-issued"); errCode(err) != "refresh_invalid" {
		t.Fatal(err)
	}
	p, _ := svc.Start("tv-1", nil)
	clk.Advance(31 * 24 * time.Hour)
	if _, err := svc.Refresh(p.RefreshToken); errCode(err) != "refresh_invalid" {
		t.Fatal("expired refresh token must fail")
	}
}

func TestOneSessionPerDevice(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	svc, store := newService(t, filepath.Join(t.TempDir(), "sessions.json"), clk)
	a, _ := svc.Start("tv-1", nil)
	rec := store.FindByHash(svc.HashRefreshToken(a.RefreshToken))
	if _, err := svc.Start("tv-1", nil); err != nil {
		t.Fatal(err)
	}
	if svc.Active(rec.ID) {
		t.Fatal("a new session must replace the device's old one")
	}
	if _, err := svc.Refresh(a.RefreshToken); errCode(err) != "refresh_invalid" {
		t.Fatal("old refresh token must be gone")
	}
}

func TestSessionsSurviveRestartAndStoreOnlyHashes(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	file := filepath.Join(t.TempDir(), "sessions.json")
	svc, store := newService(t, file, clk)
	p, _ := svc.Start("tv-1", nil)
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, file)
	if contains(raw, p.RefreshToken) {
		t.Fatal("refresh tokens must never be written in clear")
	}
	if mode := fileMode(t, file); mode != 0o600 {
		t.Fatalf("sessions file mode %o", mode)
	}
	restarted, _ := newService(t, file, clk)
	if _, err := restarted.Refresh(p.RefreshToken); err != nil {
		t.Fatalf("after restart: %v", err)
	}
}

func TestSweepDropsExpired(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	svc, store := newService(t, filepath.Join(t.TempDir(), "sessions.json"), clk)
	p, _ := svc.Start("tv-1", nil)
	id := store.FindByHash(svc.HashRefreshToken(p.RefreshToken)).ID
	clk.Advance(31 * 24 * time.Hour)
	store.Sweep(clk.Now().Unix())
	if store.FindByDevice("tv-1") != nil || svc.Active(id) {
		t.Fatal("expired session should be swept")
	}
}
