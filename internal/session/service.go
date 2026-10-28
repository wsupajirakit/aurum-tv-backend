package session

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"aurumtv/internal/auth"
	"aurumtv/internal/httpx"
	"aurumtv/internal/platform/clock"
)

const refreshTokenBytes = 32

// Pair is the response of start/refresh.
type Pair struct {
	TokenType        string `json:"tokenType"`
	AccessToken      string `json:"accessToken"`
	AccessExpiresIn  int64  `json:"accessExpiresIn"`
	RefreshToken     string `json:"refreshToken"`
	RefreshExpiresIn int64  `json:"refreshExpiresIn"`
	ServerTime       int64  `json:"serverTime"`
}

// Service runs the session lifecycle.
//
//	Start   — the app (HMAC-signed) opens a session: access token + refresh token.
//	Refresh — trade a refresh token for a new pair; tokens rotate on every use.
//
// Keeping TVs connected: the previous refresh token stays valid for the grace window (a response
// can be lost in transit), and the app can always open a new session with its HMAC key.
// Theft detection: reuse of an old refresh token after the grace window revokes the session.
type Service struct {
	store      *Store
	tokens     *auth.Tokens
	clock      clock.Clock
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	grace      time.Duration
	log        *slog.Logger
}

type Options struct {
	Store      *Store
	Tokens     *auth.Tokens
	Clock      clock.Clock
	Secret     string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	Grace      time.Duration
	Log        *slog.Logger
}

func NewService(o Options) *Service {
	return &Service{store: o.Store, tokens: o.Tokens, clock: o.Clock, secret: []byte(o.Secret),
		accessTTL: o.AccessTTL, refreshTTL: o.RefreshTTL, grace: o.Grace, log: o.Log}
}

// HashRefreshToken is HMAC-SHA256(server secret, token) in hex — the stored form.
func (s *Service) HashRefreshToken(token string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func newRefreshToken() (string, error) {
	b := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// issue rotates the refresh token. The presented hash (if any) becomes the previous token,
// valid until graceUntil (0 = now + grace).
func (s *Service) issue(r Record, presentedHash string, graceUntil int64) (*Pair, error) {
	token, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	now := clock.Unix(s.clock)
	if presentedHash != "" {
		if graceUntil == 0 {
			graceUntil = now + int64(s.grace/time.Second)
		}
		r.PrevHash = &presentedHash
		r.PrevValidUntil = graceUntil
	} else {
		r.PrevHash = nil
		r.PrevValidUntil = 0
	}
	r.CurrentHash = s.HashRefreshToken(token)
	r.RotatedAt = now
	r.ExpiresAt = now + int64(s.refreshTTL/time.Second)
	s.store.Save(r)

	access, err := s.tokens.AccessToken(r.DeviceID, r.ID)
	if err != nil {
		return nil, err
	}
	return &Pair{
		TokenType:        "Bearer",
		AccessToken:      access,
		AccessExpiresIn:  int64(s.accessTTL / time.Second),
		RefreshToken:     token,
		RefreshExpiresIn: int64(s.refreshTTL / time.Second),
		ServerTime:       now,
	}, nil
}

// Start opens a session for a device; an existing session of the same device is replaced
// (one device, one session — the file stays small).
func (s *Service) Start(deviceID string, platform *string) (*Pair, error) {
	if prev := s.store.FindByDevice(deviceID); prev != nil {
		s.store.Remove(prev.ID)
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	return s.issue(Record{ID: id, DeviceID: deviceID, Platform: platform, CreatedAt: clock.Unix(s.clock)}, "", 0)
}

func (s *Service) Refresh(refreshToken string) (*Pair, error) {
	hash := s.HashRefreshToken(refreshToken)
	rec := s.store.FindByHash(hash)
	now := clock.Unix(s.clock)
	if rec == nil || rec.ExpiresAt <= now {
		return nil, httpx.NewError(http.StatusUnauthorized, "refresh_invalid", "refresh token is invalid or expired — open a new session")
	}
	if hash == rec.CurrentHash {
		return s.issue(*rec, hash, 0)
	}
	if rec.PrevHash != nil && hash == *rec.PrevHash && now <= rec.PrevValidUntil {
		// The app missed the last response: issue a new pair without extending the old grace window.
		return s.issue(*rec, hash, rec.PrevValidUntil)
	}
	// An old token reused after the grace window: assume it was stolen and revoke the session.
	s.store.Remove(rec.ID)
	s.log.Warn("refresh token reuse detected — session revoked", "device", rec.DeviceID)
	return nil, httpx.NewError(http.StatusUnauthorized, "refresh_reused", "refresh token was already used — open a new session")
}

// Active reports whether the session behind an access token is still valid.
func (s *Service) Active(sessionID string) bool {
	return s.store.Active(sessionID, clock.Unix(s.clock))
}
