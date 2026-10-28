// Package avatar draws a fallback channel logo as SVG from the channel name.
// The colour comes from a hash of the name, so a channel always gets the same colours.
package avatar

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

const Size = 256

var palette = [][2]string{
	{"#6366f1", "#8b5cf6"}, {"#0ea5e9", "#2563eb"}, {"#10b981", "#059669"}, {"#f59e0b", "#ea580c"},
	{"#ef4444", "#e11d48"}, {"#ec4899", "#a855f7"}, {"#14b8a6", "#0891b2"}, {"#84cc16", "#16a34a"},
}

const (
	textColor = "#ffffff"
	fontStack = "system-ui, -apple-system, 'Noto Sans Thai', sans-serif"
)

// hash reproduces the original JavaScript hash over UTF-16 code units with 32-bit overflow,
// so existing channels keep their colours.
func hash(text string) int64 {
	var h int32
	for _, u := range utf16.Encode([]rune(text)) {
		h = (h << 5) - h + int32(u)
	}
	v := int64(h)
	if v < 0 {
		v = -v
	}
	return v
}

// Initial is the first character of the name, upper-cased ("?" when empty).
func Initial(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "?"
	}
	return strings.ToUpper(string([]rune(name)[0]))
}

// Colors returns the gradient pair for a name.
func Colors(name string) [2]string { return palette[hash(name)%int64(len(palette))] }

var xmlEscaper = strings.NewReplacer("<", "&lt;", ">", "&gt;", "&", "&amp;", "'", "&apos;", `"`, "&quot;")

func SVG(name string) string {
	c := Colors(name)
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 256 256" role="img" aria-label="%s">
  <defs>
    <linearGradient id="g%d" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0%%" stop-color="%s"/>
      <stop offset="100%%" stop-color="%s"/>
    </linearGradient>
  </defs>
  <rect width="256" height="256" rx="56" fill="url(#g%d)"/>
  <text x="128" y="128" fill="%s" font-family="%s"
        font-size="120" font-weight="700" text-anchor="middle" dominant-baseline="central">%s</text>
</svg>`, Size, Size, xmlEscaper.Replace(name), hash(name), c[0], c[1], hash(name), textColor, fontStack, xmlEscaper.Replace(Initial(name)))
}
