// Package hmacsig verifies HMAC-signed requests from the TV app.
//
// String to sign — the app (src/api/hmac.js) must build it byte for byte the same:
//
//	v1\n{METHOD}\n{path+query}\n{unix timestamp}\n{nonce}\n{sha256hex(body)}
//
// signature = base64url(HMAC-SHA256(secret, string)), no padding.
package hmacsig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
)

const Version = "v1"

// Request headers.
const (
	HeaderKeyID     = "X-Tv-Key-Id"
	HeaderTimestamp = "X-Tv-Timestamp"
	HeaderNonce     = "X-Tv-Nonce"
	HeaderSignature = "X-Tv-Signature"
)

type Parts struct {
	Method        string
	PathWithQuery string
	Timestamp     int64
	Nonce         string
	Body          []byte
}

func Canonical(p Parts) string {
	sum := sha256.Sum256(p.Body)
	return strings.Join([]string{
		Version,
		strings.ToUpper(p.Method),
		p.PathWithQuery,
		strconv.FormatInt(p.Timestamp, 10),
		p.Nonce,
		hex.EncodeToString(sum[:]),
	}, "\n")
}

func Sign(secret string, p Parts) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(Canonical(p)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
