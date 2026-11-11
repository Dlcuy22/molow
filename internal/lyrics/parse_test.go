package lyrics

import (
	"os"
	"path/filepath"
	"testing"
)

// ParseTest is one canonical fixture and what it should read back as. The
// fixtures cover the sidecar LRC and both word extensions a fetched sheet
// carries.
type ParseTest struct {
	name   string
	src    string
	lines  []Line
	synced bool
}

var parseTests = []ParseTest{
	{
		name: "plain lines stay unsynced",
		src:  "some lyrics\nmore lyrics",
		lines: []Line{
			{TimeMs: -1, Text: "some lyrics"},
			{TimeMs: -1, Text: "more lyrics"},
		},
	},
	{
		name:   "metadata is dropped",
		src:    "[ar:Artist]\n[00:10.00]line",
		synced: true,
		lines:  []Line{{TimeMs: 10000, Text: "line"}},
	},
	{
		name: "word block rides on the line above",
		src: "[00:04.23]just a line of text\n" +
			"<just:4.23:4.6|line of text:4.6:7.1>",
		synced: true,
		lines: []Line{{
			TimeMs: 4230,
			Text:   "just a line of text",
			Words: []Word{
				{Text: "just", StartMs: 4230, EndMs: 4600},
				{Text: "line of text", StartMs: 4600, EndMs: 7100},
			},
		}},
	},
	{
		name:   "inline rich sync carries its own times",
		src:    "<02:18.07>Yeah<02:19.34>oh<02:20.00>",
		synced: true,
		lines: []Line{{
			TimeMs: 138070,
			Text:   "Yeah oh",
			Words: []Word{
				{Text: "Yeah", StartMs: 138070, EndMs: 139340},
				{Text: "oh", StartMs: 139340, EndMs: 140000},
			},
		}},
	},
	{
		name:   "last word of a rich sync ends at the next line",
		src:    "[00:01.00]<00:01.00>hold\n[00:03.50]next",
		synced: true,
		lines: []Line{
			{TimeMs: 1000, Text: "hold", Words: []Word{{Text: "hold", StartMs: 1000, EndMs: 3500}}},
			{TimeMs: 3500, Text: "next"},
		},
	},
	{
		name:   "a lone rich sync word ends half a second on",
		src:    "[00:05.00]<00:05.00>end",
		synced: true,
		lines:  []Line{{TimeMs: 5000, Text: "end", Words: []Word{{Text: "end", StartMs: 5000, EndMs: 5500}}}},
	},
	{
		name:   "rich sync keeps kana unspaced and re-spaces latin",
		src:    "<01:00.00>そら<01:01.00>kaze<01:02.00>ok",
		synced: true,
		lines: []Line{{
			TimeMs: 60000,
			Text:   "そらkaze ok",
			Words: []Word{
				{Text: "そら", StartMs: 60000, EndMs: 61000},
				{Text: "kaze", StartMs: 61000, EndMs: 62000},
				{Text: "ok", StartMs: 62000, EndMs: 62500},
			},
		}},
	},
	{
		name:   "rich sync behind a line timestamp keeps the line time",
		src:    "[00:02.00]<00:02.00>a<00:03.00>b",
		synced: true,
		lines: []Line{{
			TimeMs: 2000,
			Text:   "a b",
			Words: []Word{
				{Text: "a", StartMs: 2000, EndMs: 3000},
				{Text: "b", StartMs: 3000, EndMs: 3500},
			},
		}},
	},
}

func TestParseFixtures(t *testing.T) {
	for _, tt := range parseTests {
		lines, synced, err := Parse(tt.src)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if synced != tt.synced {
			t.Fatalf("%s: synced = %v", tt.name, synced)
		}
		if len(lines) != len(tt.lines) {
			t.Fatalf("%s: lines = %+v", tt.name, lines)
		}
		for i, want := range tt.lines {
			got := lines[i]
			if got.TimeMs != want.TimeMs || got.Text != want.Text {
				t.Fatalf("%s: line %d = %+v, want %+v", tt.name, i, got, want)
			}
			if len(got.Words) != len(want.Words) {
				t.Fatalf("%s: line %d words = %+v, want %+v", tt.name, i, got.Words, want.Words)
			}
			for j := range want.Words {
				if got.Words[j] != want.Words[j] {
					t.Fatalf("%s: word %d.%d = %+v, want %+v", tt.name, i, j, got.Words[j], want.Words[j])
				}
			}
		}
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	src := "[00:04.23]just a line\n<just:4.230:4.600|line:4.600:5.100>\n[00:04.23]JUST"

	lines, synced, err := Parse(src)
	if err != nil || !synced {
		t.Fatalf("parse: %+v synced %v err %v", lines, synced, err)
	}
	doc := &Document{Lines: lines, Synced: true}
	if got := Serialize(doc); got != src {
		t.Fatalf("round trip:\n%q\nwant\n%q", got, src)
	}
}

// TestLoadRefRedirectsToTheVideoIdFile: a fetched sheet for a YouTube Music
// reference lands as <videoId>.lrc in the configured folder, and only there.
func TestLoadRefRedirectsToTheVideoIdFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "abc123.lrc", "[00:01.00]そら\n")

	doc, err := LoadRef("abc123", []string{dir})
	if err != nil || doc == nil {
		t.Fatalf("doc %+v err %v", doc, err)
	}
	if doc.Lines[0].Text != "そら" {
		t.Fatalf("lines: %+v", doc.Lines)
	}
	if IDPath("abc123", []string{dir}) == "" || IDPath("nope", []string{dir}) != "" {
		t.Fatal("IDPath disagrees with LoadRef")
	}
}

// TestCleanedNameFallback: a hand-written sidecar keeps the messy title the
// track file does not carry, and the loader still finds it.
func TestCleanedNameFallback(t *testing.T) {
	dir := t.TempDir()
	media := write(t, dir, "Song.flac", "")
	write(t, dir, "Song (Official MV).lrc", "[00:01.00]そら\n")

	doc, err := Load(media)
	if err != nil || doc == nil {
		t.Fatalf("doc %+v err %v", doc, err)
	}
	if doc.Lines[0].Text != "そら" {
		t.Fatalf("lines: %+v", doc.Lines)
	}
}

func TestWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "frame.lrc")

	if err := Write(path, "[00:01.00]そら"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("written file missing: %v", err)
	}
	pairs, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(pairs); n != 1 {
		t.Fatalf("temp files left behind: %d", n)
	}
}
