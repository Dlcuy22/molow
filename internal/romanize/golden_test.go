package romanize

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlcuy22/molow/internal/lyrics"
)

var update = flag.Bool("update", false, "rewrite the romanize golden files")

// TestGolden compares romanization output on real LRC fixtures; pass -update to
// accept intentional changes.
func TestGolden(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("..", "lyrics", "testdata", "romanize", "*.lrc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures found")
	}

	e := New()
	for _, path := range fixtures {
		name := strings.TrimSuffix(filepath.Base(path), ".lrc")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lines, _, err := lyrics.Parse(string(raw))
			if err != nil {
				t.Fatal(err)
			}

			var b strings.Builder
			for _, line := range lines {
				words := make([]Word, len(line.Words))
				for i, w := range line.Words {
					words[i] = Word{Text: w.Text}
				}
				res := e.Romanize(line.Text, words)
				spaced := e.RomanizeWith(line.Text, words, Options{Spacing: true})
				fmt.Fprintf(&b, "%s\n  line   %s\n", line.Text, res.Line)
				if spaced.Line != res.Line {
					fmt.Fprintf(&b, "  spaced %s\n", spaced.Line)
				}
				if len(words) > 0 {
					if !res.OK {
						b.WriteString("  words  <could not place>\n")
						continue
					}
					fmt.Fprintf(&b, "  words  %s\n", strings.Join(res.Words, "|"))
					// The sweep contract with spacing on: the same words, each a
					// contiguous slice of the spaced line.
					if !spaced.OK {
						b.WriteString("  sweeps <could not place>\n")
						continue
					}
					at := 0
					broken := ""
					for _, w := range spaced.Words {
						idx := strings.Index(spaced.Line[at:], w)
						if idx < 0 {
							broken = w
							break
						}
						at += idx + len(w)
					}
					if broken != "" {
						fmt.Fprintf(&b, "  sweeps <miss %s>\n", broken)
					} else {
						b.WriteString("  sweeps ok\n")
					}
				}
			}

			golden := strings.TrimSuffix(path, ".lrc") + ".golden"
			if *update {
				if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
					t.Fatal(err)
				}

				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/romanize -update)", err)
			}
			wantStr := strings.ReplaceAll(string(want), "\r\n", "\n")
			if b.String() != wantStr {
				t.Fatalf("output differs from %s\n--- got ---\n%s\n--- want ---\n%s", golden, b.String(), wantStr)
			}
		})
	}
}
