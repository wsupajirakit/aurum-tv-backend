package ops

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aurumtv/internal/apitest"
	"aurumtv/internal/channel"
	"aurumtv/internal/hls"
)

// server runs the real API (booted on the v1 fixture) behind a loopback listener.
func server(t *testing.T, vars map[string]string) (*apitest.Env, *Admin) {
	t.Helper()
	env := apitest.Boot(t, apitest.FixtureV1, vars)
	srv := httptest.NewServer(env.Handler)
	t.Cleanup(srv.Close)
	admin, err := NewAdmin(srv.URL, apitest.APIKey, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return env, admin
}

func quiet() Logger { return Logger{Out: io.Discard, Err: io.Discard, Tag: "test"} }

func TestAdminKeyNeverTravelsOverPlainHTTP(t *testing.T) {
	for _, u := range []string{"http://tv.example.com", "ftp://x", "not a url", "http://localhost.evil.com"} {
		if _, err := NewAdmin(u, "k", time.Second); !errors.Is(err, ErrConfig) {
			t.Errorf("%s accepted", u)
		}
	}
	for _, u := range []string{"https://tv.example.com/", "http://localhost:3000", "http://127.0.0.1:8080"} {
		if _, err := NewAdmin(u, "k", time.Second); err != nil {
			t.Errorf("%s rejected: %v", u, err)
		}
	}
}

func TestWrongAdminKeyFails(t *testing.T) {
	_, admin := server(t, nil)
	admin.key = "wrong"
	var apiErr *APIError
	if _, err := admin.ListChannels(context.Background()); !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("%v", err)
	}
}

func TestDiscoverAddsOnlyVerifiedSources(t *testing.T) {
	env, admin := server(t, nil)
	ch, err := env.Channels.Create(channel.Raw{Name: "Mirror TV", URL: "https://cdn.test/live-as/abc-1/playlist.m3u8"})
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := LoadRules("")
	plays := map[string]bool{"https://cdn.test/live-as/abc-3/playlist.m3u8": true}
	probe := func(_ context.Context, u string) hls.Result { return hls.Result{OK: plays[u], Resolution: "1024x576"} }

	if err := Discover(context.Background(), admin, DiscoverOptions{Rules: rules, Concurrency: 2, Probe: probe}, quiet()); err != nil {
		t.Fatal(err)
	}
	if len(env.Channels.Get(ch.ID).Sources) != 1 {
		t.Fatal("report mode must not change anything")
	}
	if err := Discover(context.Background(), admin, DiscoverOptions{Apply: true, ChannelID: ch.ID, Rules: rules, Concurrency: 2, Probe: probe}, quiet()); err != nil {
		t.Fatal(err)
	}
	got := env.Channels.Get(ch.ID).Sources
	if len(got) != 2 || got[0].URL != ch.Sources[0].URL || got[1].URL != "https://cdn.test/live-as/abc-3/playlist.m3u8" || got[1].Label != "mirror 576p" {
		t.Fatalf("%+v", got)
	}
}

func TestRulesAreValidated(t *testing.T) {
	for _, doc := range []string{`{"rules":[{"match":"(","range":[1,2]}]}`, `{"rules":[{"match":"x","range":[1,999]}]}`, `{"rules":[{"match":"x","range":[5,1]}]}`, `nope`} {
		if _, err := ParseRules([]byte(doc)); !errors.Is(err, ErrConfig) {
			t.Errorf("%s accepted", doc)
		}
	}
}

func TestSyncUpdatesOnlyTheResolverSource(t *testing.T) {
	fresh := "https://ch3-33-web.cdn.byteark.com/live/playlist.m3u8?x_ark_expires=4102444800&x_ark_signature=new"
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><script id="__NEXT_DATA__" type="application/json">{"props":{"initialState":{"liveReducer":{"live":{"streamUrlWebSVOD":"`+fresh+`"}}}}}</script></html>`)
	}))
	defer page.Close()
	env, admin := server(t, nil)
	before := env.Channels.Get(apitest.DynamicID)
	o := SyncOptions{SourceURL: page.URL, Timeout: 5 * time.Second, Client: page.Client()}

	if r, err := SyncCh3(context.Background(), admin, SyncOptions{SourceURL: page.URL, Timeout: time.Second, Client: page.Client(), DryRun: true}, quiet()); err != nil || r != SyncDryRun {
		t.Fatal(r, err)
	}
	if r, err := SyncCh3(context.Background(), admin, o, quiet()); err != nil || r != SyncUpdated {
		t.Fatal(r, err)
	}
	after := env.Channels.Get(apitest.DynamicID)
	if after.URL != fresh || after.Name != before.Name || len(after.Sources) != len(before.Sources) {
		t.Fatalf("%+v", after)
	}
	if r, _ := SyncCh3(context.Background(), admin, o, quiet()); r != SyncUpToDate {
		t.Fatal("second run must be a no-op")
	}
}

func TestSyncRejectsNonHLSLinks(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<script id="__NEXT_DATA__">{"props":{"initialState":{"liveReducer":{"live":{"streamUrlWebSVOD":"http://evil.test/x.m3u8"}}}}}</script>`)
	}))
	defer page.Close()
	_, admin := server(t, nil)
	if _, err := SyncCh3(context.Background(), admin, SyncOptions{SourceURL: page.URL, Timeout: time.Second, Client: page.Client()}, quiet()); err == nil {
		t.Fatal("plain-http link accepted")
	}
}

func TestRestoreIsIdempotent(t *testing.T) {
	env, admin := server(t, nil)
	if err := RestoreChannels(context.Background(), admin, false, quiet()); err != nil {
		t.Fatal(err)
	}
	count := env.Channels.Meta().Count
	if count <= 4 {
		t.Fatalf("seed channels were not added: %d", count)
	}
	if err := RestoreChannels(context.Background(), admin, false, quiet()); err != nil || env.Channels.Meta().Count != count {
		t.Fatal("second run must add nothing")
	}
}

func TestSmokePassesAgainstTheRealAPI(t *testing.T) {
	env := apitest.Boot(t, apitest.FixtureV1, nil)
	srv := httptest.NewServer(env.Handler)
	defer srv.Close()
	var out bytes.Buffer
	if n := Smoke(context.Background(), SmokeOptions{BaseURL: srv.URL, KeyID: apitest.KeyID, Secret: apitest.Secret, Client: srv.Client()}, &out); n != 0 {
		t.Fatalf("%d failure(s)\n%s", n, out.String())
	}
	if strings.Count(out.String(), "PASS") < 7 {
		t.Fatalf("not every check ran:\n%s", out.String())
	}
	var bad bytes.Buffer
	if Smoke(context.Background(), SmokeOptions{BaseURL: srv.URL, KeyID: apitest.KeyID, Secret: "wrong", Client: srv.Client()}, &bad) == 0 {
		t.Fatal("a wrong secret must fail")
	}
}

func TestGenKey(t *testing.T) {
	var a, b bytes.Buffer
	if GenKey("tv2", &a) != nil || GenKey("tv2", &b) != nil || a.String() == b.String() || !strings.Contains(a.String(), "HMAC_KEYS=tv2:") {
		t.Fatal("keys must be fresh each time")
	}
	if !errors.Is(GenKey("bad id!", io.Discard), ErrConfig) {
		t.Fatal("invalid key id accepted")
	}
}
