package hls

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"syscall"
	"time"
)

// Options of a probe.
type Options struct {
	Timeout          time.Duration
	MaxPlaylistBytes int64
	SegmentBytes     int64
	Variant          string // "lowest" (default, saves bandwidth) or "highest"
	Headers          map[string]string
	Client           *http.Client // injected in tests
	Now              func() time.Time
}

// Result never carries an error value: a failure is described by Stage + Reason.
type Result struct {
	OK           bool    `json:"ok"`
	URL          string  `json:"url"`
	Stage        string  `json:"stage"`
	Reason       *string `json:"reason"`
	LatencyMs    int64   `json:"latencyMs"`
	CheckedAt    string  `json:"checkedAt"`
	Live         bool    `json:"live,omitempty"`
	Resolution   string  `json:"resolution,omitempty"`
	Bandwidth    int     `json:"bandwidth,omitempty"`
	SegmentBytes int     `json:"segmentBytes,omitempty"`
}

var mp4Boxes = [][]byte{[]byte("ftyp"), []byte("styp"), []byte("moof"), []byte("sidx"), []byte("moov")}

// looksLikeMedia: MPEG-TS sync byte, an fMP4 box, or an audio-only segment (ID3/ADTS).
func looksLikeMedia(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if b[0] == 0x47 {
		return true
	}
	if len(b) >= 8 {
		for _, box := range mp4Boxes {
			if bytes.Equal(b[4:8], box) {
				return true
			}
		}
	}
	if bytes.HasPrefix(b, []byte("ID3")) {
		return true
	}
	return len(b) >= 2 && b[0] == 0xff && b[1]&0xf0 == 0xf0
}

// Probe verifies that url really plays, the way a player would:
//  1. the playlist must be #EXTM3U;
//  2. a master playlist → pick a variant and load its media playlist;
//  3. the media playlist must list segments;
//  4. the first bytes of the latest segment must be real media.
func Probe(ctx context.Context, rawURL string, o Options) Result {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	started := now()
	done := func(r Result) Result {
		r.URL = rawURL
		r.LatencyMs = now().Sub(started).Milliseconds()
		r.CheckedAt = now().UTC().Format("2006-01-02T15:04:05.000Z")
		return r
	}
	fail := func(stage, reason string) Result { return done(Result{Stage: stage, Reason: &reason}) }

	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	first, finalURL, err := fetch(ctx, client, rawURL, o.Headers, "", o.MaxPlaylistBytes)
	if err != nil {
		return fail(stageOf(err, "playlist"), reasonOf(err))
	}
	if !IsPlaylist(string(first)) {
		return fail("playlist", "not_hls")
	}
	pl := Parse(string(first))
	mediaURL := finalURL
	var chosen *Variant
	if pl.Master {
		if len(pl.Variants) == 0 {
			return fail("master", "no_variants")
		}
		v := pickVariant(pl.Variants, o.Variant)
		chosen = &v
		if mediaURL, err = resolve(v.URI, finalURL); err != nil {
			return fail("master", "bad_variant_uri")
		}
		body, final, err := fetch(ctx, client, mediaURL, o.Headers, "", o.MaxPlaylistBytes)
		if err != nil {
			return fail(stageOf(err, "variant"), reasonOf(err))
		}
		if !IsPlaylist(string(body)) {
			return fail("variant", "not_hls")
		}
		pl, mediaURL = Parse(string(body)), final
		if pl.Master {
			return fail("variant", "nested_master")
		}
	}
	if len(pl.Segments) == 0 {
		return fail("media", "no_segments")
	}
	segURL, err := resolve(pl.Segments[len(pl.Segments)-1], mediaURL)
	if err != nil {
		return fail("media", "bad_segment_uri")
	}
	seg, _, err := fetch(ctx, client, segURL, o.Headers, fmt.Sprintf("bytes=0-%d", o.SegmentBytes-1), o.SegmentBytes)
	if err != nil {
		return fail(stageOf(err, "segment"), reasonOf(err))
	}
	if !looksLikeMedia(seg) {
		return fail("segment", "not_media")
	}
	r := Result{OK: true, Stage: "segment", Live: pl.Live, SegmentBytes: len(seg)}
	if chosen != nil {
		r.Resolution, r.Bandwidth = chosen.Resolution, chosen.Bandwidth
	}
	return done(r)
}

type httpStatusError struct{ code int }

func (e httpStatusError) Error() string { return fmt.Sprintf("http_%d", e.code) }

type networkError struct{ err error }

func (e networkError) Error() string { return e.err.Error() }
func (e networkError) Unwrap() error { return e.err }

// fetch reads at most limit bytes and then closes the connection — a segment is never downloaded whole.
func fetch(ctx context.Context, client *http.Client, u string, headers map[string]string, rangeHeader string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", networkError{err}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, "", networkError{err}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, "", httpStatusError{res.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, "", networkError{err}
	}
	return body, res.Request.URL.String(), nil
}

func stageOf(err error, stage string) string {
	var ne networkError
	if errors.As(err, &ne) {
		return "network"
	}
	return stage
}

func reasonOf(err error) string {
	var he httpStatusError
	if errors.As(err, &he) {
		return he.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return "timeout"
	}
	return "network:" + networkCode(err)
}

// networkCode is a short, URL-free cause (the raw error text embeds the signed stream url).
func networkCode(err error) string {
	var dns *net.DNSError
	var errno syscall.Errno
	var tlsErr *tls.CertificateVerificationError
	switch {
	case errors.As(err, &dns):
		return "ENOTFOUND"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "ECONNREFUSED"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "ECONNRESET"
	case errors.As(err, &tlsErr):
		return "CERT_INVALID"
	case errors.As(err, &errno):
		return "E" + strconv.Itoa(int(errno))
	}
	return "error"
}

func pickVariant(vs []Variant, strategy string) Variant {
	sorted := append([]Variant(nil), vs...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Bandwidth < sorted[b].Bandwidth })
	if strategy == "highest" {
		return sorted[len(sorted)-1]
	}
	return sorted[0]
}

func resolve(ref, base string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}
