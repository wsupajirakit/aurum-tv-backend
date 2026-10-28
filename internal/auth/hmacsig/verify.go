package hmacsig

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aurumtv/internal/auth"
	"aurumtv/internal/httpx"
	"aurumtv/internal/platform/clock"
)

// Rejection reasons, sent to the app as the error code.
const (
	ReasonMissing    = "signature_missing"
	ReasonIncomplete = "signature_incomplete"
	ReasonUnknownKey = "unknown_key"
	ReasonClockSkew  = "clock_skew"
	ReasonBadNonce   = "invalid_nonce"
	ReasonBadSig     = "bad_signature"
	ReasonReplayed   = "replayed"
)

const (
	nonceMinLen   = 16
	nonceMaxLen   = 128
	nonceCacheMax = 100_000
)

var nonceCharset = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type Verifier struct {
	keys    map[string]string
	maxSkew time.Duration
	clock   clock.Clock
	nonces  *NonceCache
}

func NewVerifier(keys map[string]string, maxSkew time.Duration, c clock.Clock) *Verifier {
	return &Verifier{keys: keys, maxSkew: maxSkew, clock: c, nonces: NewNonceCache(nonceCacheMax)}
}

// Result of a verification; Reason is empty when OK.
type Result struct {
	OK     bool
	KeyID  string
	Reason string
}

func fail(reason string) Result { return Result{Reason: reason} }

// PathWithQuery is the request target as signed: escaped path plus "?query" when present.
func PathWithQuery(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return r.URL.EscapedPath()
	}
	return r.URL.EscapedPath() + "?" + r.URL.RawQuery
}

// Verify checks the signature headers against the buffered body.
func (v *Verifier) Verify(r *http.Request) Result {
	h := func(name string) string { return strings.TrimSpace(r.Header.Get(name)) }
	keyID, ts, nonce, sig := h(HeaderKeyID), h(HeaderTimestamp), h(HeaderNonce), h(HeaderSignature)

	switch {
	case keyID == "" && sig == "":
		return fail(ReasonMissing)
	case keyID == "" || ts == "" || nonce == "" || sig == "":
		return fail(ReasonIncomplete)
	}
	secret, ok := v.keys[keyID]
	if !ok {
		return fail(ReasonUnknownKey)
	}
	timestamp, err := strconv.ParseInt(ts, 10, 64)
	now := v.clock.Now()
	if err != nil || abs(now.Unix()-timestamp) > int64(v.maxSkew/time.Second) {
		return fail(ReasonClockSkew)
	}
	if len(nonce) < nonceMinLen || len(nonce) > nonceMaxLen || !nonceCharset.MatchString(nonce) {
		return fail(ReasonBadNonce)
	}
	body := []byte{}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		body = httpx.Body(r)
	}
	expected := Sign(secret, Parts{Method: r.Method, PathWithQuery: PathWithQuery(r), Timestamp: timestamp, Nonce: nonce, Body: body})
	if !auth.SafeEqual(expected, sig) {
		return fail(ReasonBadSig)
	}
	// Remember the nonce only after the signature is valid: without the key nobody can fill the cache.
	if !v.nonces.Remember(nonce, now, 2*v.maxSkew) {
		return fail(ReasonReplayed)
	}
	return Result{OK: true, KeyID: keyID}
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
