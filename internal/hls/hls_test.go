package hls

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	master = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720\nhi/index.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=500000,RESOLUTION=426x240\nlo/index.m3u8\n"
	media  = "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nseg1.ts\n#EXTINF:6,\nseg2.ts\n"
)

var ts = append([]byte{0x47, 0x40, 0x00, 0x10}, make([]byte, 60)...)

func serve(t *testing.T, routes map[string]func(http.ResponseWriter)) (*httptest.Server, *[]string) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if h, ok := routes[r.URL.Path]; ok {
			h(w)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func text(s string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { _, _ = w.Write([]byte(s)) }
}
func status(c int) func(http.ResponseWriter) { return func(w http.ResponseWriter) { w.WriteHeader(c) } }

var opts = Options{Timeout: 300 * time.Millisecond, MaxPlaylistBytes: 1 << 20, SegmentBytes: 1024}

func TestParse(t *testing.T) {
	if !IsPlaylist("\uFEFF#EXTM3U\n") || IsPlaylist("<html>") {
		t.Fatal("IsPlaylist")
	}
	m := Parse(master)
	if !m.Master || len(m.Variants) != 2 || m.Variants[1].Resolution != "426x240" {
		t.Fatalf("master %+v", m)
	}
	p := Parse(media)
	if p.Master || len(p.Segments) != 2 || !p.Live || p.TargetDuration != 6 {
		t.Fatalf("media %+v", p)
	}
	if Parse(media + "#EXT-X-ENDLIST\n").Live {
		t.Fatal("VOD is not live")
	}
}

func TestProbePlaysThroughToTheLatestSegment(t *testing.T) {
	srv, calls := serve(t, map[string]func(http.ResponseWriter){
		"/live/playlist.m3u8": text(master), "/live/lo/index.m3u8": text(media), "/live/lo/seg2.ts": func(w http.ResponseWriter) { _, _ = w.Write(ts) },
	})
	r := Probe(context.Background(), srv.URL+"/live/playlist.m3u8", opts)
	if !r.OK || r.Resolution != "426x240" {
		t.Fatalf("%+v", r)
	}
	want := []string{"/live/playlist.m3u8", "/live/lo/index.m3u8", "/live/lo/seg2.ts"}
	for i, p := range want {
		if (*calls)[i] != p {
			t.Fatalf("calls %v", *calls)
		}
	}
}

func TestProbeFailuresAreSpecific(t *testing.T) {
	cases := []struct {
		routes        map[string]func(http.ResponseWriter)
		stage, reason string
	}{
		{map[string]func(http.ResponseWriter){}, "playlist", "http_404"},
		{map[string]func(http.ResponseWriter){"/p.m3u8": text("<html>blocked</html>")}, "playlist", "not_hls"},
		{map[string]func(http.ResponseWriter){"/p.m3u8": text("#EXTM3U\n#EXT-X-ENDLIST\n")}, "media", "no_segments"},
		{map[string]func(http.ResponseWriter){"/p.m3u8": text(media), "/seg2.ts": status(403)}, "segment", "http_403"},
		{map[string]func(http.ResponseWriter){"/p.m3u8": text(media), "/seg2.ts": text("<html>error</html>")}, "segment", "not_media"},
		{map[string]func(http.ResponseWriter){"/p.m3u8": func(http.ResponseWriter) { time.Sleep(600 * time.Millisecond) }}, "network", "timeout"},
	}
	for _, c := range cases {
		srv, _ := serve(t, c.routes)
		r := Probe(context.Background(), srv.URL+"/p.m3u8", opts)
		if r.OK || r.Stage != c.stage || r.Reason == nil || *r.Reason != c.reason {
			t.Errorf("want %s:%s got ok=%v %s:%v", c.stage, c.reason, r.OK, r.Stage, deref(r.Reason))
		}
	}
}

func TestLooksLikeMedia(t *testing.T) {
	fmp4 := append([]byte{0, 0, 0, 24}, []byte("ftypisom")...)
	for _, b := range [][]byte{ts, fmp4, []byte("ID3\x04"), {0xff, 0xf1}} {
		if !looksLikeMedia(b) {
			t.Errorf("%x should be media", b[:4])
		}
	}
	if looksLikeMedia([]byte("<html>")) || looksLikeMedia(nil) {
		t.Error("html is not media")
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestNetworkReasonNeverLeaksTheURL(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL + "/p.m3u8?x_ark_signature=secret"
	srv.Close()
	r := Probe(context.Background(), u, opts)
	if r.Stage != "network" || *r.Reason != "network:ECONNREFUSED" {
		t.Fatalf("%s %s", r.Stage, *r.Reason)
	}
}
