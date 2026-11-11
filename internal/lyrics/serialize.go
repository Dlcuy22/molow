package lyrics

import (
	"fmt"
	"strings"
)

// Serialize writes a document as one canonical extended-LRC string and is the
// reverse of Parse. A timed line is `[mm:ss.cc]text`, a line with words
// carries its word block right after it, and an untimed document becomes
// plain text. Fetched sheets are saved in this exact shape, so a restart reads
// them through the sidecar loader.
func Serialize(doc *Document) string {
	if doc == nil || len(doc.Lines) == 0 {
		return ""
	}

	var b strings.Builder
	for _, line := range doc.Lines {
		if line.TimeMs < 0 {
			b.WriteString(line.Text)
			b.WriteByte('\n')

			continue
		}
		b.WriteString(formatTime(line.TimeMs))
		b.WriteString(line.Text)
		b.WriteByte('\n')
		if len(line.Words) > 0 {
			b.WriteByte('<')
			for i, w := range line.Words {
				if i > 0 {
					b.WriteByte('|')
				}
				b.WriteString(w.Text)
				fmt.Fprintf(&b, ":%.3f:%.3f", float64(w.StartMs)/1000, float64(w.EndMs)/1000)
			}
			b.WriteString(">\n")
		}
		if line.Translation != "" {
			b.WriteString(formatTime(line.TimeMs))
			b.WriteString(line.Translation)
			b.WriteByte('\n')
		}
	}

	return strings.TrimSpace(b.String())
}

// formatTime is the `[mm:ss.cc]` prefix the parser and every LRC reader know.
func formatTime(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	m := ms / 60000
	s := (ms % 60000) / 1000
	c := (ms % 1000) / 10

	return fmt.Sprintf("[%02d:%02d.%02d]", m, s, c)
}

// Quality scores a raw payload before any parsing, on the string itself: 0 no
// content, 1 plain text, 2 line-synced LRC, 3 word-synced. The chain prefers
// the number, not the provider that produced it.
func Quality(raw string) int {
	src := strings.TrimSpace(raw)
	if src == "" {
		return 0
	}
	if hasWords(src) {
		return 3
	}
	if timestamp.MatchString(src) {
		return 2
	}

	return 1
}

// hasWords reports whether string carries word timing: a continuation block
// `<text:startSec:endSec|...>` or an inline rich sync with `<mm:ss.xx>` tags.
func hasWords(src string) bool {
	if wordBlockLine.MatchString(src) {
		return true
	}

	if !strings.ContainsRune(src, '<') {
		return false
	}
	return wordTagTime.MatchString(strings.TrimSpace(src))
}
