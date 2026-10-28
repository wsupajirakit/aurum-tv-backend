package api_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"aurumtv/internal/apitest"
	"aurumtv/internal/auth/hmacsig"
)

const (
	testKeyID   = apitest.KeyID
	testSecret  = apitest.Secret
	testAPIKey  = apitest.APIKey
	dynamicID   = apitest.DynamicID
	staticID    = "static-ch-0001"
	syncURLGood = "https://ch3-33-web.cdn.byteark.com/live/playlist.m3u8?x_ark_expires=9999999999&x_ark_signature=abc"
)

type env struct {
	*apitest.Env
	t *testing.T
}

type probeTable = apitest.ProbeTable

var fakeProbe = apitest.FakeProbe

func boot(t *testing.T, vars map[string]string) *env {
	t.Helper()
	return &env{Env: apitest.Boot(t, apitest.FixtureV1, vars), t: t}
}

type response struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (e *env) do(method, target string, body any, headers map[string]string) response {
	e.t.Helper()
	var payload []byte
	switch b := body.(type) {
	case nil:
	case string:
		payload = []byte(b)
	default:
		payload, _ = json.Marshal(b)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.Handler.ServeHTTP(rec, req)
	res := response{status: rec.Code, header: rec.Header(), raw: rec.Body.Bytes()}
	_ = json.Unmarshal(rec.Body.Bytes(), &res.body)
	return res
}

func nonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// signedHeaders builds the app's signature headers independently of the server code under test.
func signedHeaders(method, target string, body []byte, opts ...func(*sigOpts)) map[string]string {
	o := sigOpts{secret: testSecret, keyID: testKeyID, ts: time.Now().Unix(), nonce: nonce()}
	for _, f := range opts {
		f(&o)
	}
	sig := hmacsig.Sign(o.secret, hmacsig.Parts{Method: method, PathWithQuery: target, Timestamp: o.ts, Nonce: o.nonce, Body: body})
	return map[string]string{"X-TV-Key-Id": o.keyID, "X-TV-Timestamp": strconv.FormatInt(o.ts, 10), "X-TV-Nonce": o.nonce, "X-TV-Signature": sig}
}

type sigOpts struct {
	secret, keyID, nonce string
	ts                   int64
}

func (e *env) signed(method, target string, body any, token string, opts ...func(*sigOpts)) response {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	h := signedHeaders(method, target, payload, opts...)
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	var b any
	if payload != nil {
		b = string(payload)
	}
	return e.do(method, target, b, h)
}

func (e *env) session(device string) map[string]any {
	e.t.Helper()
	res := e.signed("POST", "/api/session", map[string]any{"deviceId": device}, "")
	if res.status != http.StatusCreated {
		e.t.Fatalf("session: %d %s", res.status, res.raw)
	}
	return res.body
}

func admin() map[string]string { return map[string]string{"X-API-Key": testAPIKey} }

func code(r response) string { s, _ := r.body["code"].(string); return s }

func expect(t *testing.T, got, want any, msg string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", msg, got, want)
	}
}
