// Package hls parses just enough of HLS playlists to verify a stream really plays.
package hls

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"
)

type Variant struct {
	URI        string
	Bandwidth  int
	Resolution string
}

// Playlist is either a master playlist (Variants) or a media playlist (Segments).
type Playlist struct {
	Master         bool
	Variants       []Variant
	Segments       []string
	Live           bool
	TargetDuration int
}

var attrPattern = regexp.MustCompile(`([A-Z0-9-]+)=("[^"]*"|[^,]*)`)

// IsPlaylist reports whether text is an HLS playlist (#EXTM3U first, leading BOM/space allowed).
func IsPlaylist(text string) bool {
	return strings.HasPrefix(strings.TrimLeft(strings.TrimPrefix(text, "\uFEFF"), " \t\r\n"), "#EXTM3U")
}

func Parse(text string) Playlist {
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	nextURI := func(i int) string {
		for _, l := range lines[i+1:] {
			if !strings.HasPrefix(l, "#") {
				return l
			}
		}
		return ""
	}

	var p Playlist
	for i, l := range lines {
		if strings.HasPrefix(l, "#EXT-X-STREAM-INF") {
			p.Master = true
			uri := nextURI(i)
			if uri == "" {
				continue
			}
			v := Variant{URI: uri}
			_, attrs, _ := strings.Cut(l, ":")
			for _, m := range attrPattern.FindAllStringSubmatch(attrs, -1) {
				val := strings.Trim(m[2], `"`)
				switch m[1] {
				case "BANDWIDTH":
					v.Bandwidth, _ = strconv.Atoi(val)
				case "RESOLUTION":
					v.Resolution = val
				}
			}
			p.Variants = append(p.Variants, v)
		}
	}
	if p.Master {
		return p
	}
	p.Live = true
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "#EXTINF"):
			if uri := nextURI(i); uri != "" {
				p.Segments = append(p.Segments, uri)
			}
		case strings.HasPrefix(l, "#EXT-X-TARGETDURATION"):
			_, v, _ := strings.Cut(l, ":")
			p.TargetDuration, _ = strconv.Atoi(v)
		case l == "#EXT-X-ENDLIST":
			p.Live = false
		}
	}
	return p
}
