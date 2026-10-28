package hmacsig

import (
	"testing"
	"time"
)

// Shared vector: the app (scripts/verify-signer.mjs) must produce the same signature.
// The expected value was computed independently with `openssl dgst -sha256 -hmac`.
func TestSignatureVector(t *testing.T) {
	got := Sign("vector-secret", Parts{Method: "POST", PathWithQuery: "/api/channels/abc/sync?x=1", Timestamp: 1700000000,
		Nonce: "abcdefghijklmnop", Body: []byte(`{"url":"https://x.byteark.com/a.m3u8"}`)})
	if want := "I6hID3u0f3PSSR8lo7Xxihrki3VC4ye0l7Bwt1FcBJE"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestNonceCacheReplayExpiryAndCap(t *testing.T) {
	c := NewNonceCache(2)
	now := time.Unix(1000, 0)
	if !c.Remember("a", now, time.Minute) || c.Remember("a", now, time.Minute) {
		t.Fatal("second use must be a replay")
	}
	if !c.Remember("a", now.Add(2*time.Minute), time.Minute) {
		t.Fatal("expired nonce may be used again")
	}
	c.Remember("b", now.Add(2*time.Minute), time.Minute)
	c.Remember("c", now.Add(2*time.Minute), time.Minute)
	if c.Len() != 2 {
		t.Fatalf("cap not enforced: %d", c.Len())
	}
}
