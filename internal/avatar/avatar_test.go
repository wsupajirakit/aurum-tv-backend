package avatar

import "testing"

// Reference values computed with the original JavaScript hash — existing channels keep their colours.
func TestHashMatchesJavaScript(t *testing.T) {
	for name, want := range map[string]int64{"3HD": 51311, "ไทยรัฐทีวี 32": 245928352, "One 31": 1928635784, "?": 63, "Thai PBS": 1053312061, "😀 emoji": 1572615351} {
		if got := hash(name); got != want {
			t.Errorf("%q: got %d want %d", name, got, want)
		}
	}
	if Colors("Thai PBS") != palette[5] {
		t.Error("palette index mismatch")
	}
}

func TestInitialAndEscaping(t *testing.T) {
	if Initial("  ") != "?" || Initial("ไทย") != "ไ" || Initial("one") != "O" {
		t.Fatal("initial")
	}
	if svg := SVG(`<script>"x"</script>`); !contains(svg, "&lt;script&gt;") || contains(svg, "<script>") {
		t.Fatal("name must be XML-escaped")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
