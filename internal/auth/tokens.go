package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"aurumtv/internal/platform/clock"
)

// Token roles. Device tokens cannot be used on admin endpoints and vice versa.
const (
	RoleDevice    = "device"
	RoleAdmin     = "admin"
	AdminSubject  = "tv-admin"
	AudienceTVApp = "tv-app"
)

// Claims of every token this service issues (HS256).
type Claims struct {
	Role string `json:"role"`
	SID  string `json:"sid,omitempty"`
	jwt.RegisteredClaims
}

// Tokens issues and verifies JWTs.
type Tokens struct {
	secret    []byte
	clock     clock.Clock
	accessTTL time.Duration
	adminTTL  time.Duration
}

func NewTokens(secret string, c clock.Clock, accessTTL, adminTTL time.Duration) *Tokens {
	return &Tokens{secret: []byte(secret), clock: c, accessTTL: accessTTL, adminTTL: adminTTL}
}

func (t *Tokens) sign(claims Claims, ttl time.Duration) (string, error) {
	now := t.clock.Now().Truncate(time.Second)
	claims.IssuedAt = jwt.NewNumericDate(now)
	claims.ExpiresAt = jwt.NewNumericDate(now.Add(ttl))
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

func (t *Tokens) AdminToken() (string, error) {
	return t.sign(Claims{Role: RoleAdmin, RegisteredClaims: jwt.RegisteredClaims{Subject: AdminSubject}}, t.adminTTL)
}

func (t *Tokens) AccessToken(deviceID, sessionID string) (string, error) {
	return t.sign(Claims{
		Role:             RoleDevice,
		SID:              sessionID,
		RegisteredClaims: jwt.RegisteredClaims{Subject: deviceID, Audience: jwt.ClaimStrings{AudienceTVApp}},
	}, t.accessTTL)
}

// Verify parses a token, accepting HS256 only and checking expiry against the injected clock.
func (t *Tokens) Verify(token string) (*Claims, error) {
	if token == "" {
		return nil, errors.New("empty token")
	}
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(t.clock.Now),
	)
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// BearerToken extracts the token from "Authorization: Bearer <token>".
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// HasAudience reports whether the claims target aud.
func (c *Claims) HasAudience(aud string) bool {
	for _, a := range c.Audience {
		if a == aud {
			return true
		}
	}
	return false
}
