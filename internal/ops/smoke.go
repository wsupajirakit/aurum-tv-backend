package ops

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aurumtv/internal/auth/hmacsig"
	"aurumtv/internal/channel"
)

// SmokeOptions points the smoke test at a running server.
type SmokeOptions struct {
	BaseURL, KeyID, Secret string
	// RealSource also makes the server fetch a fresh link from the real stream source.
	RealSource bool
	Client     *http.Client
}

// smoke calls the API exactly like the TV app: HMAC-signed requests plus a session.
type smoke struct {
	SmokeOptions
	offset   int64
	failures int
	out      io.Writer
}

type reply struct {
	status int
	body   map[string]any
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *smoke) call(ctx context.Context, method, path string, in any, token string) reply {
	var payload []byte
	if in != nil {
		payload, _ = json.Marshal(in)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return reply{}
	}
	if s.KeyID != "" && s.Secret != "" {
		ts, nonce := time.Now().Unix()+s.offset, randomToken(16)
		req.Header.Set(hmacsig.HeaderKeyID, s.KeyID)
		req.Header.Set(hmacsig.HeaderTimestamp, strconv.FormatInt(ts, 10))
		req.Header.Set(hmacsig.HeaderNonce, nonce)
		req.Header.Set(hmacsig.HeaderSignature, hmacsig.Sign(s.Secret, hmacsig.Parts{Method: method, PathWithQuery: path, Timestamp: ts, Nonce: nonce, Body: payload}))
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := s.Client.Do(req)
	if err != nil {
		return reply{}
	}
	defer res.Body.Close()
	r := reply{status: res.StatusCode}
	_ = json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(&r.body)
	return r
}

func (s *smoke) check(label string, ok bool, detail string) {
	mark := "PASS"
	if !ok {
		mark = "FAIL"
		s.failures++
	}
	if detail != "" {
		detail = "  (" + detail + ")"
	}
	fmt.Fprintf(s.out, "%s  %s%s\n", mark, label, detail)
}

func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }

var expiresParam = regexp.MustCompile(`x_ark_expires=\d+`)

// Smoke runs the end-to-end checks and returns the number of failures. It never edits other
// channels: sync sends the resolver channel's current url back.
func Smoke(ctx context.Context, o SmokeOptions, out io.Writer) int {
	s := &smoke{SmokeOptions: o, out: out}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")

	health := s.call(ctx, http.MethodGet, "/api/health", nil, "")
	serverTime, _ := health.body["serverTime"].(float64)
	s.check("health + serverTime", health.body["ok"] == true && serverTime > 0, "")
	s.offset = int64(serverTime) - time.Now().Unix()

	device := make([]byte, 6)
	_, _ = rand.Read(device)
	session := s.call(ctx, http.MethodPost, "/api/session", map[string]any{"deviceId": "smoke-" + hex.EncodeToString(device), "platform": "smoke"}, "")
	s.check("open a session", session.status == http.StatusCreated && str(session.body, "accessToken") != "", fmt.Sprintf("HTTP %d", session.status))

	rotated := s.call(ctx, http.MethodPost, "/api/session/refresh", map[string]any{"refreshToken": str(session.body, "refreshToken")}, "")
	s.check("rotate the refresh token", rotated.status == http.StatusOK && str(rotated.body, "refreshToken") != str(session.body, "refreshToken"), fmt.Sprintf("HTTP %d", rotated.status))
	token := str(rotated.body, "accessToken")

	unsigned := (&smoke{SmokeOptions: SmokeOptions{BaseURL: s.BaseURL, Client: s.Client}}).call(ctx, http.MethodGet, "/api/channels?enabled=true", nil, "")
	s.check("a client without a key (old APK) cannot read the list", unsigned.status == http.StatusUnauthorized, fmt.Sprintf("HTTP %d", unsigned.status))

	list := s.call(ctx, http.MethodGet, "/api/channels?enabled=true", nil, token)
	channels, _ := list.body["channels"].([]any)
	allHaveSources := len(channels) > 0
	var resolverChannel map[string]any
	for _, c := range channels {
		ch, _ := c.(map[string]any)
		srcs, _ := ch["sources"].([]any)
		allHaveSources = allHaveSources && len(srcs) > 0
		for _, x := range srcs {
			if src, _ := x.(map[string]any); str(src, "resolver") == channel.ResolverCh3Plus && resolverChannel == nil {
				resolverChannel = ch
			}
		}
	}
	s.check("channel list carries sources and status", allHaveSources, fmt.Sprintf("%d channel(s)", len(channels)))

	logged := s.call(ctx, http.MethodPost, "/api/logs", map[string]any{"level": "info", "tag": "Smoke", "state": "SMOKE_TEST", "message": "smoke test"}, token)
	s.check("signed log upload", logged.status == http.StatusOK, fmt.Sprintf("HTTP %d", logged.status))

	if resolverChannel != nil {
		id := str(resolverChannel, "id")
		if o.RealSource {
			refreshed := s.call(ctx, http.MethodPost, "/api/channels/"+id+"/refresh", nil, token)
			ch, _ := refreshed.body["channel"].(map[string]any)
			s.check("renew the resolver channel from the real source", refreshed.status == http.StatusOK && expiresParam.MatchString(str(ch, "url")), "")
		}
		var current string
		srcs, _ := resolverChannel["sources"].([]any)
		for _, x := range srcs {
			if src, _ := x.(map[string]any); str(src, "resolver") == channel.ResolverCh3Plus {
				current = str(src, "url")
			}
		}
		if current != "" {
			sync := s.call(ctx, http.MethodPost, "/api/channels/"+id+"/sync", map[string]any{"url": current}, token)
			s.check("sync the resolver url (unchanged url)", sync.status == http.StatusOK || str(sync.body, "code") == "host_not_allowed", fmt.Sprintf("HTTP %d", sync.status))
		}
	}
	return s.failures
}
