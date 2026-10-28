// Package stream fetches signed stream links from a source page and validates synced links.
package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultSourceURL is the page that publishes the current Channel 3 stream link.
const DefaultSourceURL = "https://ch3plus.com/live"

const (
	nextDataTag  = `<script id="__NEXT_DATA__"`
	scriptClose  = `</script>`
	maxPageBytes = 8 << 20
)

// BrowserHeaders are sent to the source page (it serves the link only to browsers).
var BrowserHeaders = map[string]string{
	"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Accept":     "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
}

// ExtractStreamURL reads the stream url from the page's __NEXT_DATA__ JSON (index/slice parsing,
// no backtracking regex over large HTML). It prefers the 1080p web link. "" when not found.
func ExtractStreamURL(html string) string {
	idx := strings.Index(html, nextDataTag)
	if idx == -1 {
		return ""
	}
	gt := strings.Index(html[idx:], ">")
	if gt == -1 {
		return ""
	}
	start := idx + gt + 1
	end := strings.Index(html[start:], scriptClose)
	if end == -1 {
		return ""
	}
	var doc struct {
		Props struct {
			InitialState struct {
				LiveReducer struct {
					Live struct {
						WebSVOD string `json:"streamUrlWebSVOD"`
						App     string `json:"streamUrlApp"`
						Default string `json:"streamUrl"`
					} `json:"live"`
				} `json:"liveReducer"`
			} `json:"initialState"`
		} `json:"props"`
	}
	if json.Unmarshal([]byte(html[start:start+end]), &doc) != nil {
		return ""
	}
	live := doc.Props.InitialState.LiveReducer.Live
	for _, u := range []string{live.WebSVOD, live.App, live.Default} {
		if u != "" {
			if strings.Contains(u, "m3u8") {
				return u
			}
			return ""
		}
	}
	return ""
}

// Page is the fetched source page.
type Page struct {
	Status int
	HTML   string
}

// FetchPage loads the source page with a timeout.
func FetchPage(ctx context.Context, client *http.Client, sourceURL string, timeout time.Duration) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return Page{}, err
	}
	for k, v := range BrowserHeaders {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxPageBytes))
	if err != nil {
		return Page{}, err
	}
	return Page{Status: res.StatusCode, HTML: string(body)}, nil
}

// FreshURL fetches the page and extracts the current stream url.
func FreshURL(ctx context.Context, client *http.Client, sourceURL string, timeout time.Duration) (string, error) {
	page, err := FetchPage(ctx, client, sourceURL, timeout)
	if err != nil {
		return "", err
	}
	if page.Status < 200 || page.Status > 299 {
		return "", fmt.Errorf("source page answered HTTP %d", page.Status)
	}
	u := ExtractStreamURL(page.HTML)
	if u == "" {
		return "", fmt.Errorf("no stream url in the source page")
	}
	return u, nil
}

var expiresPattern = regexp.MustCompile(`x_ark_expires=(\d+)`)

// ExpiresAt reads the expiry (unix seconds) of a signed url; ok=false when the url has none.
func ExpiresAt(u string) (int64, bool) {
	m := expiresPattern.FindStringSubmatch(u)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	return n, err == nil
}

// Expiring reports whether a signed url expires within threshold. Urls without an expiry never expire.
func Expiring(u string, threshold time.Duration, now time.Time) bool {
	exp, ok := ExpiresAt(u)
	if !ok {
		return false
	}
	return exp <= now.Add(threshold).Unix()
}
