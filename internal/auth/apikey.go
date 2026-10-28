// Package auth holds admin and app authentication.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

const HeaderAPIKey = "X-Api-Key"

// SafeEqual compares two strings in constant time. Both sides are hashed first so lengths match
// and the comparison never leaks the secret's length.
func SafeEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// APIKeys validates admin keys (accepted in the X-API-Key header only — never in URLs, which end up in logs).
type APIKeys struct{ keys []string }

func NewAPIKeys(keys []string) *APIKeys { return &APIKeys{keys: keys} }

func (k *APIKeys) Configured() bool { return len(k.keys) > 0 }

// Valid checks every configured key without stopping early, so timing does not reveal which key matched.
func (k *APIKeys) Valid(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || len(k.keys) == 0 {
		return false
	}
	matched := false
	for _, key := range k.keys {
		if SafeEqual(key, candidate) {
			matched = true
		}
	}
	return matched
}

func (k *APIKeys) FromRequest(r *http.Request) bool { return k.Valid(r.Header.Get(HeaderAPIKey)) }
