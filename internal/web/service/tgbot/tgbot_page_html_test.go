package tgbot

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// pageIsWellFormed reports whether a page opens and closes <code> and <b> evenly,
// keeps whole UTF-8 characters and never cuts an entity.
func pageIsWellFormed(page string) bool {
	if strings.Count(page, "<code>") != strings.Count(page, "</code>") ||
		strings.Count(page, "<b>") != strings.Count(page, "</b>") || !utf8.ValidString(page) {
		return false
	}
	return strings.Count(page, "&") == strings.Count(page, "&amp;")
}

// A line longer than a page (a long link in <code>) was cut at a byte offset:
// the pieces had an unclosed <code>, a split entity or a split character, and
// Telegram rejected them with "can't parse entities".
func TestOverlongHTMLLineIsSplitIntoValidPieces(t *testing.T) {
	for name, line := range map[string]string{
		"code link":     "<code>vless://" + strings.Repeat("a", 2500) + "</code>",
		"entities":      "<code>" + strings.Repeat("x&amp;", 700) + "</code>",
		"multibyte":     "<b>" + strings.Repeat("я", 1500) + "</b>",
		"text then tag": strings.Repeat("t", 900) + "<code>" + strings.Repeat("c", 900) + "</code>",
	} {
		pages := pageMessage(line, botCaptionLimit)
		if len(pages) < 2 {
			t.Fatalf("%s: not split", name)
		}
		for i, p := range pages {
			if len(p) > botCaptionLimit {
				t.Errorf("%s: page %d is %d bytes", name, i, len(p))
			}
			if !pageIsWellFormed(p) {
				t.Errorf("%s: page %d is not valid HTML on its own: %q...%q", name, i, p[:min(40, len(p))], p[max(0, len(p)-40):])
			}
		}
	}
}
