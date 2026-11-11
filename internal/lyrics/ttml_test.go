package lyrics

import "testing"

// A word-synced TTML may time each character and mark the gaps with spans
// that hold only a space. The line text has to keep those gaps, or the reader
// sees every letter spaced out.
func TestTTMLKeepsTheSpacingItsSpansCarry(t *testing.T) {
	src := `<tt><body><div><p begin="00:36.22">` +
		`<span begin="00:36.228" end="00:36.635">H</span>` +
		`<span begin="00:36.635" end="00:36.685">a</span>` +
		`<span begin="00:36.902" end="00:37.234"> </span>` +
		`<span begin="00:37.234" end="00:37.367">a</span>` +
		`<span begin="00:37.367" end="00:37.467">i</span>` +
		`</p></div></body></tt>`

	doc := ParseTTML(src)
	if doc == nil || len(doc.Lines) != 1 {
		t.Fatalf("doc = %+v, want one line", doc)
	}
	line := doc.Lines[0]
	if line.Text != "Ha ai" {
		t.Fatalf("text = %q, want the span spacing kept", line.Text)
	}
	if len(line.Words) != 4 {
		t.Fatalf("words = %d, want the four sung characters", len(line.Words))
	}
	for _, w := range line.Words {
		if w.Text == "" {
			t.Fatal("a spacing span must not become a word")
		}
	}
}

// Word spans are separated by text nodes, while the syllables of one word sit
// in adjacent spans with nothing between them. The line text has to keep that
// difference exactly.
func TestTTMLKeepsSpacesBetweenSpansAndJoinsSyllables(t *testing.T) {
	src := `<tt><body><div><p begin="00:18.907">` +
		`<span begin="18.907" end="19.276">We're</span> ` +
		`<span begin="19.276" end="19.613">both</span> ` +
		`<span begin="19.613" end="20.626">strangers</span> ` +
		`<span begin="20.626" end="21.223">to</span> ` +
		`<span begin="21.223" end="21.507">a</span> ` +
		`<span begin="21.507" end="21.874">mir</span>` +
		`<span begin="21.874" end="22.043">a</span>` +
		`<span begin="22.043" end="23.452">cle</span>` +
		`</p></div></body></tt>`

	doc := ParseTTML(src)
	if doc == nil || len(doc.Lines) != 1 {
		t.Fatalf("doc = %+v, want one line", doc)
	}
	line := doc.Lines[0]
	const want = "We're both strangers to a miracle"
	if line.Text != want {
		t.Fatalf("text = %q, want %q", line.Text, want)
	}
	if len(line.Words) != 8 {
		t.Fatalf("words = %d, want the eight sung segments", len(line.Words))
	}
}

// Japanese spans carry no spaces at all, and none may appear.
func TestTTMLKeepsKanaUnspaced(t *testing.T) {
	src := `<tt><body><div><p begin="00:01">` +
		`<span begin="00:01" end="00:02">あ</span>` +
		`<span begin="00:02" end="00:03">の</span>` +
		`<span begin="00:03" end="00:04">ね</span>` +
		`</p></div></body></tt>`

	doc := ParseTTML(src)
	if doc == nil {
		t.Fatal("no document")
	}
	if got := doc.Lines[0].Text; got != "あのね" {
		t.Fatalf("text = %q, want the kana joined", got)
	}
}
