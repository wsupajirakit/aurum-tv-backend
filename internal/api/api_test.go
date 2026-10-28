package api_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- enforce mode (default) ----------

func TestKeylessClientsAreRejectedAndSignedAppReads(t *testing.T) {
	e := boot(t, nil)
	for _, target := range []string{"/api/channels", "/api/channels/" + dynamicID, "/api/events"} {
		res := e.do("GET", target, nil, nil)
		expect(t, res.status, 401, target)
		expect(t, code(res), "signature_missing", target)
	}
	s := e.session("tv-device-0001")
	res := e.signed("GET", "/api/channels?enabled=true", nil, s["accessToken"].(string))
	expect(t, res.status, 200, "signed read")
	channels := res.body["channels"].([]any)
	expect(t, channels[2].(map[string]any)["id"], dynamicID, "migrated order keeps Channel 3 third")
	if res.header.Get("X-Tv-Server-Time") == "" {
		t.Fatal("missing server time header")
	}
	expect(t, code(e.signed("GET", "/api/channels", nil, "")), "access_token_missing", "signed without token")
	expect(t, e.do("GET", "/api/channels", nil, admin()).status, 200, "admin key reads")
	expect(t, e.do("GET", "/api/health", nil, nil).status, 200, "health stays open")
}

func TestSignatureFailures(t *testing.T) {
	e := boot(t, nil)
	device := map[string]any{"deviceId": "tv-device-0001"}
	expect(t, code(e.do("POST", "/api/session", device, nil)), "signature_missing", "unsigned")
	expect(t, code(e.signed("POST", "/api/session", device, "", func(o *sigOpts) { o.secret = "wrong" })), "bad_signature", "wrong secret")

	payload, _ := json.Marshal(device)
	h := signedHeaders("POST", "/api/session", payload)
	expect(t, code(e.do("POST", "/api/session", `{"deviceId":"tv-device-evil"}`, h)), "bad_signature", "tampered body")

	skew := e.signed("POST", "/api/session", device, "", func(o *sigOpts) { o.ts = time.Now().Unix() - 3600 })
	expect(t, code(skew), "clock_skew", "clock skew")
	if _, ok := skew.body["serverTime"].(float64); !ok {
		t.Fatal("clock_skew must carry serverTime")
	}
	expect(t, code(e.signed("POST", "/api/session", device, "", func(o *sigOpts) { o.keyID = "nope" })), "unknown_key", "unknown key")
	expect(t, code(e.signed("POST", "/api/session", device, "", func(o *sigOpts) { o.nonce = "short" })), "invalid_nonce", "bad nonce")

	h = signedHeaders("POST", "/api/session", payload)
	expect(t, e.do("POST", "/api/session", string(payload), h).status, 201, "first use")
	expect(t, code(e.do("POST", "/api/session", string(payload), h)), "replayed", "replay")
}

func TestSyncNeedsSignatureTokenAndAllowedHost(t *testing.T) {
	e := boot(t, nil)
	path := "/api/channels/" + dynamicID + "/sync"
	expect(t, code(e.signed("POST", path, map[string]any{"url": syncURLGood}, "")), "access_token_missing", "no token")
	token := e.session("tv-device-0001")["accessToken"].(string)
	ok := e.signed("POST", path, map[string]any{"url": syncURLGood}, token)
	expect(t, ok.status, 200, "sync")
	expect(t, ok.body["channel"].(map[string]any)["url"], syncURLGood, "synced url becomes the active url")

	expect(t, code(e.signed("POST", path, map[string]any{"url": "https://evil.example.com/live/playlist.m3u8"}, token)), "host_not_allowed", "foreign host")
	expect(t, code(e.signed("POST", path, map[string]any{"url": "http://ch3-33-web.cdn.byteark.com/live/playlist.m3u8"}, token)), "https_required", "http")
	other := e.signed("POST", "/api/channels/"+staticID+"/sync", map[string]any{"url": syncURLGood}, token)
	expect(t, other.status, 403, "static channel refuses sync")
	expect(t, code(other), "not_dynamic", "static channel code")
}

func TestRefreshRotationGraceAndReuse(t *testing.T) {
	e := boot(t, map[string]string{"REFRESH_GRACE_SEC": "60"})
	first := e.session("tv-device-0001")
	r1 := e.signed("POST", "/api/session/refresh", map[string]any{"refreshToken": first["refreshToken"]}, "")
	expect(t, r1.status, 200, "rotate")
	if r1.body["refreshToken"] == first["refreshToken"] {
		t.Fatal("refresh token must rotate")
	}
	retry := e.signed("POST", "/api/session/refresh", map[string]any{"refreshToken": first["refreshToken"]}, "")
	expect(t, retry.status, 200, "previous token within the grace window")
	bogus := e.signed("POST", "/api/session/refresh", map[string]any{"refreshToken": strings.Repeat("x", 43)}, "")
	expect(t, code(bogus), "refresh_invalid", "bogus token")
}

func TestReuseAfterGraceRevokesSession(t *testing.T) {
	e := boot(t, map[string]string{"REFRESH_GRACE_SEC": "0"})
	first := e.session("tv-device-0001")
	second := e.signed("POST", "/api/session/refresh", map[string]any{"refreshToken": first["refreshToken"]}, "")
	time.Sleep(1100 * time.Millisecond)
	reused := e.signed("POST", "/api/session/refresh", map[string]any{"refreshToken": first["refreshToken"]}, "")
	expect(t, code(reused), "refresh_reused", "reuse after grace")
	after := e.signed("POST", "/api/logs", map[string]any{"message": "x"}, second.body["accessToken"].(string))
	expect(t, code(after), "session_revoked", "revoked session's access token")
}

func TestReplacedSessionAndTokenSeparation(t *testing.T) {
	e := boot(t, nil)
	old := e.session("tv-device-0001")
	fresh := e.session("tv-device-0001")
	expect(t, code(e.signed("POST", "/api/logs", map[string]any{"message": "x"}, old["accessToken"].(string))), "session_revoked", "replaced session")
	expect(t, e.signed("POST", "/api/logs", map[string]any{"message": "hello"}, fresh["accessToken"].(string)).status, 200, "fresh session")
	asAdmin := e.do("GET", "/api/logs", nil, map[string]string{"Authorization": "Bearer " + fresh["accessToken"].(string)})
	expect(t, asAdmin.status, 401, "device token on admin endpoint")
}

func TestDefaultModeRejectsKeylessWrites(t *testing.T) {
	e := boot(t, nil)
	for _, target := range []string{"/api/channels/" + dynamicID + "/sync", "/api/logs", "/api/channels/" + dynamicID + "/refresh"} {
		expect(t, e.do("POST", target, map[string]any{"url": syncURLGood, "message": "x"}, nil).status, 401, target)
	}
}

// ---------- log mode (debugging only) ----------

func TestLogModeAllowsUnsignedButKeepsHostRules(t *testing.T) {
	e := boot(t, map[string]string{"HMAC_MODE": "log", "LOG_MAX_BATCH": "3"})
	path := "/api/channels/" + dynamicID + "/sync"
	expect(t, e.do("POST", path, map[string]any{"url": syncURLGood}, nil).status, 200, "unsigned sync in log mode")
	expect(t, e.do("POST", "/api/logs", map[string]any{"level": "info", "message": "old apk"}, nil).status, 200, "unsigned log")
	expect(t, e.do("POST", path, map[string]any{"url": "https://evil.example.com/a.m3u8"}, nil).status, 400, "foreign host still refused")

	batch := e.do("POST", "/api/logs", map[string]any{"logs": []any{
		map[string]any{"message": "m0"}, map[string]any{"message": "m1"}, map[string]any{"message": "m2"},
		map[string]any{"message": "m3"}, map[string]any{"message": "m4"}}}, nil)
	expect(t, batch.body["count"], 3.0, "batch capped")
	expect(t, batch.body["dropped"], 2.0, "dropped reported")

	big := e.do("POST", "/api/logs", map[string]any{"message": strings.Repeat("a", 10_000),
		"details": map[string]any{"blob": strings.Repeat("b", 20_000)}}, nil)
	entry := big.body["entry"].(map[string]any)
	expect(t, len([]rune(entry["message"].(string))), 2000, "message truncated")
	expect(t, entry["details"].(map[string]any)["truncated"], true, "details truncated")
}

// ---------- admin ----------

func adminJWT(t *testing.T, e *env) string {
	t.Helper()
	res := e.do("POST", "/api/auth/token", nil, admin())
	expect(t, res.status, 200, "admin token")
	expect(t, res.body["expiresIn"], "7d", "expiresIn format")
	return res.body["token"].(string)
}

func TestAdminLogsAndHardening(t *testing.T) {
	e := boot(t, nil)
	token := adminJWT(t, e)
	bearer := map[string]string{"Authorization": "Bearer " + token}
	expect(t, e.do("GET", "/api/logs?date=x/../../../channels", nil, bearer).status, 400, "path traversal")
	expect(t, e.do("GET", "/api/logs", nil, bearer).status, 200, "today's logs")
	toggled := e.do("POST", "/api/logs/toggle", nil, bearer)
	expect(t, toggled.body["enabled"], false, "toggle without body flips")
	e.do("POST", "/api/logs/toggle", nil, bearer)
	cfg := e.do("GET", "/api/logs/config", nil, bearer)
	if _, leaked := cfg.body["logsDir"]; leaked {
		t.Fatal("server path must not be exposed")
	}
	expect(t, e.do("DELETE", "/api/logs/not-a-date", nil, bearer).status, 400, "delete validates date")

	expect(t, e.do("POST", "/api/channels?api_key="+testAPIKey, map[string]any{"name": "x", "url": "https://a.example/b.m3u8"}, nil).status, 401, "key in query refused")
	expect(t, e.do("GET", "/api/test/ch3", nil, nil).status, 404, "diagnostics off by default")

	logo := e.do("GET", "/api/channels/"+dynamicID+"/logo", nil, nil)
	expect(t, logo.status, 200, "logo")
	expect(t, logo.header.Get("Cross-Origin-Resource-Policy"), "cross-origin", "CORP")
	expect(t, logo.header.Get("X-Content-Type-Options"), "nosniff", "nosniff")
	if !strings.HasPrefix(logo.header.Get("Content-Type"), "image/svg+xml") {
		t.Fatal("logo must be svg")
	}
	nf := e.do("GET", "/api/nope", nil, nil)
	expect(t, nf.status, 404, "unknown route")
	if len(nf.body) != 2 || nf.body["code"] != "not_found" {
		t.Fatalf("404 body leaks or wrong shape: %v", nf.body)
	}
	expect(t, e.do("PUT", "/api/health", nil, nil).status, 404, "unknown method answers 404 JSON")
}

func TestAdminChannelCRUDAndValidation(t *testing.T) {
	e := boot(t, nil)
	bad := e.do("PATCH", "/api/channels/"+staticID, map[string]any{"sources": []any{map[string]any{"id": "x"}}}, admin())
	expect(t, bad.status, 400, "static source without url")
	expect(t, e.do("PATCH", "/api/channels/"+staticID, map[string]any{}, admin()).status, 400, "empty patch")
	expect(t, e.do("POST", "/api/channels", map[string]any{"name": "x", "logo": "javascript:alert(1)", "url": "https://a.example/a.m3u8"}, admin()).status, 400, "logo scheme")
	expect(t, e.do("POST", "/api/channels", map[string]any{"name": "x", "url": "https://a.example/a.m3u8"}, nil).status, 401, "missing key")
	expect(t, e.do("POST", "/api/channels", map[string]any{"name": "x", "url": "https://a.example/a.m3u8"}, map[string]string{"X-API-Key": "nope"}).status, 403, "wrong key")

	created := e.do("POST", "/api/channels", map[string]any{"name": "New", "url": "https://a.example/new.m3u8"}, admin())
	expect(t, created.status, 201, "create")
	id := created.body["id"].(string)
	expect(t, created.body["logoUrl"], "http://example.com/api/channels/"+id+"/logo", "logo fallback url")
	srcs := created.body["sources"].([]any)
	expect(t, srcs[0].(map[string]any)["id"], "primary", "bare url becomes the primary source")

	patched := e.do("PATCH", "/api/channels/"+id, map[string]any{"number": 31, "sources": []any{
		map[string]any{"id": "main", "url": "https://a.example/main.m3u8"}, map[string]any{"id": "b1", "url": "https://b.example/b.m3u8"}}}, admin())
	expect(t, patched.status, 200, "patch sources")
	expect(t, patched.body["number"], 31.0, "number")
	expect(t, patched.body["url"], "https://a.example/main.m3u8", "url follows the active source")

	urlOnly := e.do("PATCH", "/api/channels/"+id, map[string]any{"url": "https://new.example/x.m3u8"}, admin())
	expect(t, urlOnly.body["sources"].([]any)[0].(map[string]any)["url"], "https://new.example/x.m3u8", "url-only edit updates the active source")
	expect(t, urlOnly.body["sources"].([]any)[1].(map[string]any)["url"], "https://b.example/b.m3u8", "backups untouched")

	bulk := e.do("POST", "/api/channels/bulk", map[string]any{"channels": []any{
		map[string]any{"name": "B1", "url": "https://a.example/b1.m3u8"}, map[string]any{"name": "B2", "sources": []any{map[string]any{"url": "https://a.example/b2.m3u8"}}}}}, admin())
	expect(t, bulk.body["added"], 2.0, "bulk (sources-only items accepted)")
	reordered := e.do("POST", "/api/channels/reorder", map[string]any{"ids": []string{id}}, admin())
	expect(t, reordered.body["channels"].([]any)[0].(map[string]any)["id"], id, "reorder")
	del := e.do("DELETE", "/api/channels/"+id, nil, admin())
	expect(t, del.body["deleted"], true, "delete")
	expect(t, e.do("DELETE", "/api/channels/"+id, nil, admin()).status, 404, "delete again")
}

// ---------- self-healing endpoints ----------

func TestReportExpeditesARealCheckAndFailsOver(t *testing.T) {
	e := boot(t, nil)
	created := e.do("POST", "/api/channels", map[string]any{"name": "Heal", "sources": []any{
		map[string]any{"id": "main", "url": "https://a.test/main.m3u8"}, map[string]any{"id": "b1", "url": "https://b.test/b1.m3u8"}}}, admin())
	id := created.body["id"].(string)
	var calls []string
	e.Monitor.UseProbe(fakeProbe(probeTable{"https://a.test/main.m3u8": false, "https://b.test/b1.m3u8": true}, &calls))

	token := e.session("heal-device-0001")["accessToken"].(string)
	read := e.signed("GET", "/api/channels/"+id, nil, token)
	for _, s := range read.body["sources"].([]any) {
		if s.(map[string]any)["status"] == nil {
			t.Fatal("sources must carry a status")
		}
	}
	first := e.signed("POST", "/api/channels/"+id+"/report", map[string]any{"sourceId": "main", "reason": "timeout"}, token)
	expect(t, first.status, 202, "report accepted")
	waitFor(t, func() bool { return e.Channels.Get(id).ActiveID() == "b1" })
	expect(t, e.signed("POST", "/api/channels/"+id+"/report", map[string]any{}, token).body["accepted"], false, "cooldown")
}

func TestAdminProbeAndOverview(t *testing.T) {
	e := boot(t, nil)
	created := e.do("POST", "/api/channels", map[string]any{"name": "Probe", "sources": []any{
		map[string]any{"id": "main", "url": "https://a.test/p.m3u8"}, map[string]any{"id": "b1", "url": "https://b.test/p.m3u8"}}}, admin())
	id := created.body["id"].(string)
	e.Monitor.UseProbe(fakeProbe(probeTable{"https://a.test/p.m3u8": true, "https://b.test/p.m3u8": true}, nil))
	probed := e.do("POST", "/api/channels/"+id+"/probe", nil, admin())
	for _, s := range probed.body["sources"].([]any) {
		expect(t, s.(map[string]any)["health"].(map[string]any)["status"], "up", "probed status")
	}
	overview := e.do("GET", "/api/health/sources", nil, admin())
	expect(t, overview.status, 200, "overview")
	expect(t, e.do("GET", "/api/health/sources", nil, nil).status, 401, "overview needs the key")
}

func TestMigrationWritesV2WithBackup(t *testing.T) {
	e := boot(t, nil)
	data, err := os.ReadFile(filepath.Join(e.DataDir, "channels.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Version  int `json:"version"`
		Channels []struct {
			Name    string `json:"name"`
			Order   int    `json:"order"`
			Sources []struct {
				Resolver string `json:"resolver"`
			} `json:"sources"`
		} `json:"channels"`
	}
	_ = json.Unmarshal(data, &saved)
	expect(t, saved.Version, 2, "version")
	for _, c := range saved.Channels {
		if c.Name == "3HD" {
			expect(t, c.Sources[0].Resolver, "ch3plus", "Channel 3 resolver")
			expect(t, c.Order, 2, "Channel 3 position")
		}
	}
	if _, err := os.Stat(filepath.Join(e.DataDir, "channels.bak.json")); err != nil {
		t.Fatal("the previous file must be backed up")
	}
}

func TestBodyLimits(t *testing.T) {
	e := boot(t, map[string]string{"HMAC_MODE": "log", "LOG_BODY_LIMIT_BYTES": "100"})
	expect(t, e.do("POST", "/api/logs", map[string]any{"message": strings.Repeat("x", 500)}, nil).status, http.StatusRequestEntityTooLarge, "log body limit")
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
