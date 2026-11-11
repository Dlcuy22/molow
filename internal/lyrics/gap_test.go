package lyrics

import (
	"fmt"
	"strings"
	"testing"
)

// sec renders milliseconds as the decimal seconds a word block carries.
func sec(ms int) string {
	return fmt.Sprintf("%.3f", float64(ms)/1000)
}

func wordLine(ms int, text string, end int) Line {
	return Line{TimeMs: int64(ms), Text: text, Words: []Word{
		{Text: text, StartMs: int64(ms), EndMs: int64(end)},
	}}
}

// A word-timed line knows where it ends, so the mark starts exactly there.
func TestMarkInstrumentalUsesTheWordEnd(t *testing.T) {
	lines := []Line{
		wordLine(0, "one", 2000),
		wordLine(2000+instrumentalGapMs+1000, "two", 2000+instrumentalGapMs+2000),
	}

	got := MarkInstrumental(lines, 0)
	if len(got) != 3 {
		t.Fatalf("lines = %d, want one mark", len(got))
	}
	if got[1].Text != InstrumentalMark || got[1].TimeMs != 2000 {
		t.Fatalf("mark = %q at %d", got[1].Text, got[1].TimeMs)
	}
}

func TestMarkInstrumentalLeavesShortGapsAlone(t *testing.T) {
	lines := []Line{
		wordLine(0, "one", 2000),
		wordLine(2000+instrumentalGapMs, "two", 2000+instrumentalGapMs+1000),
	}
	if got := MarkInstrumental(lines, 0); len(got) != len(lines) {
		t.Fatalf("lines = %d, want no mark at the threshold", len(got))
	}
}

// A line-timed sheet has no end, so the end is estimated from the sheet's own
// pacing: here every line is 20 characters over 2 s, and the long jump to the
// last line is the silence.
func TestMarkInstrumentalEstimatesLineTimedEnds(t *testing.T) {
	const text = "aaaaaaaaaaaaaaaaaaaa" // 20 characters
	lines := []Line{
		{TimeMs: 0, Text: text},
		{TimeMs: 2000, Text: text},
		{TimeMs: 4000, Text: text},
		{TimeMs: 6000, Text: text},
		{TimeMs: 18000, Text: "last"},
	}

	got := MarkInstrumental(lines, 0)
	if len(got) != 6 {
		t.Fatalf("lines = %d, want one mark", len(got))
	}
	mark := got[4]
	if mark.Text != InstrumentalMark {
		t.Fatalf("mark = %q", mark.Text)
	}
	if mark.TimeMs < 7000 || mark.TimeMs > 9000 {
		t.Fatalf("mark at %d, want the estimated end of the 6 s line", mark.TimeMs)
	}
}

// Without enough short gaps there is no pacing to measure, so a line-timed
// sheet keeps no marks rather than guessing.
func TestMarkInstrumentalNeedsPacingForLineTimedSheets(t *testing.T) {
	lines := []Line{
		{TimeMs: 0, Text: "one"},
		{TimeMs: 20000, Text: "two"},
	}
	if got := MarkInstrumental(lines, 0); len(got) != len(lines) {
		t.Fatalf("lines = %d, want no estimate without pacing", len(got))
	}
}

// The track's own length fills the end when the sheet stops early.
func TestMarkInstrumentalFillsTheTail(t *testing.T) {
	lines := []Line{wordLine(0, "one", 166896)}
	got := MarkInstrumental(lines, 187000)
	if len(got) != 2 {
		t.Fatalf("lines = %d, want one tail mark", len(got))
	}
	if last := got[len(got)-1]; last.Text != InstrumentalMark || last.TimeMs != 166896 {
		t.Fatalf("tail = %q at %d", last.Text, last.TimeMs)
	}
}

func TestMarkInstrumentalLeavesAShortTailAlone(t *testing.T) {
	lines := []Line{wordLine(0, "one", 166896)}
	if got := MarkInstrumental(lines, 166896+instrumentalGapMs); len(got) != len(lines) {
		t.Fatalf("lines = %d, want no tail", len(got))
	}
}

// A mark already in the input is recomputed, not stacked. A stale mark in a
// short gap disappears.
func TestMarkInstrumentalRecomputesExistingMarks(t *testing.T) {
	lines := []Line{
		wordLine(0, "one", 2000),
		{TimeMs: 2000, Text: InstrumentalMark},
		wordLine(3000, "two", 4000),
	}

	got := MarkInstrumental(lines, 0)
	marks := 0
	for _, line := range got {
		if line.Text == InstrumentalMark {
			marks++
		}
	}
	if marks != 0 {
		t.Fatalf("marks = %d, want the stale one dropped for a short gap", marks)
	}
}

// The full path: a TTML sheet is parsed, then marked in memory. The sidecar
// text itself never gains a mark.
func TestParseTTMLThenMarkInstrumental(t *testing.T) {
	const end = 2000
	src := `<tt><body><div>` +
		`<p begin="0.000" end="` + sec(end) + `">` +
		`<span begin="0.000" end="` + sec(end) + `">one</span></p>` +
		`<p begin="` + sec(end+instrumentalGapMs+1000) + `" end="` + sec(end+instrumentalGapMs+2000) + `">` +
		`<span begin="` + sec(end+instrumentalGapMs+1000) + `" end="` + sec(end+instrumentalGapMs+2000) + `">two</span></p>` +
		`</div></body></tt>`

	doc := ParseTTML(src)
	if doc == nil {
		t.Fatal("no document")
	}
	raw := Serialize(doc)
	if strings.Contains(raw, InstrumentalMark) {
		t.Fatal("the serialized sheet must not carry a mark")
	}

	got := MarkInstrumental(doc.Lines, 0)
	if len(got) != len(doc.Lines)+1 || got[1].Text != InstrumentalMark {
		t.Fatalf("marked lines = %+v", got)
	}
}

// The PetitLyrics path writes no marks either; the read path adds them.
func TestPetitSheetIsMarkedOnlyInMemory(t *testing.T) {
	const end = 2500
	next := end + instrumentalGapMs + 1000
	wsy := `<wsy><linenum>2</linenum>` +
		`<line><linestring>one</linestring><starttime>0</starttime><wordnum>1</wordnum>` +
		`<word><starttime>0</starttime><endtime>` + fmt.Sprint(end) + `</endtime><wordstring>one</wordstring></word></line>` +
		`<line><linestring>two</linestring><starttime>` + fmt.Sprint(next) + `</starttime><wordnum>1</wordnum>` +
		`<word><starttime>` + fmt.Sprint(next) + `</starttime><endtime>` + fmt.Sprint(next+1000) + `</endtime><wordstring>two</wordstring></word></line></wsy>`

	raw := petitToLRC(mustParseWSY(t, wsy))
	if strings.Contains(raw, InstrumentalMark) {
		t.Fatalf("the written sheet must not carry a mark:\n%s", raw)
	}

	lines, _, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := MarkInstrumental(lines, 0); len(got) != 3 {
		t.Fatalf("marked lines = %d, want one in-memory mark", len(got))
	}
}

func mustParseWSY(t *testing.T, src string) []petitLine {
	t.Helper()
	lines, err := parseWSY([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	return lines
}
