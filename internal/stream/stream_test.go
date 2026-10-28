package stream

import (
	"testing"
	"time"
)

func TestExtractStreamURL(t *testing.T) {
	page := `<html><script id="__NEXT_DATA__" type="application/json">{"props":{"initialState":{"liveReducer":{"live":{"streamUrlWebSVOD":"https://x.byteark.com/live/playlist.m3u8?x_ark_expires=1"}}}}}</script></html>`
	if got := ExtractStreamURL(page); got != "https://x.byteark.com/live/playlist.m3u8?x_ark_expires=1" {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{"<html></html>", `<script id="__NEXT_DATA__">{broken</script>`,
		`<script id="__NEXT_DATA__">{"props":{"initialState":{"liveReducer":{"live":{"streamUrl":"https://x/a.mp4"}}}}}</script>`} {
		if ExtractStreamURL(bad) != "" {
			t.Errorf("expected no url for %q", bad)
		}
	}
}

func TestExpiry(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	if !Expiring("https://a/b.m3u8?x_ark_expires=1000060", 2*time.Hour, now) {
		t.Error("expiring soon")
	}
	if Expiring("https://a/b.m3u8?x_ark_expires=1100000", 2*time.Hour, now) || Expiring("https://a/b.m3u8", time.Hour, now) {
		t.Error("not expiring")
	}
}

func TestValidateSyncURL(t *testing.T) {
	hosts := []string{"byteark.com"}
	cases := map[string]string{
		"https://ch3-33-web.cdn.byteark.com/live/playlist.m3u8?t=1": "",
		"":                                "missing_url",
		"not a url":                       "invalid_url",
		"http://x.byteark.com/a.m3u8":     "https_required",
		"https://x.byteark.com/a.mp4":     "not_m3u8",
		"https://evil.example.com/a.m3u8": "host_not_allowed",
		"https://bytearkxcom.evil.example/a.m3u8": "host_not_allowed",
	}
	for in, want := range cases {
		if _, got := ValidateSyncURL(in, hosts); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
