package backend

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlcuy22/molow/internal/lyrics"
)

func lyricFixture(t *testing.T, root, body string) string {
	t.Helper()
	media := filepath.Join(root, "Album", "01. Song.flac")
	if err := lyrics.Write(filepath.Join(root, "Album", "Song.lrc"), body); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	return media
}

func TestLyricsRomanizationOffLeavesTheDocumentAlone(t *testing.T) {
	root := isolateConfig(t)
	svc := newPlayerService()
	svc.player = newFakePlayer()

	media := lyricFixture(t, root,
		"[00:01.00]涙はそっと\n<涙:1.0:1.3|は:1.3:1.6|そっと:1.6:2.4>\n")
	svc.appSettings.Playback.Lyrics.AutoRomanization = false

	doc, err := svc.Lyrics(media)
	if err != nil || doc == nil {
		t.Fatalf("Lyrics: doc %+v err %v", doc, err)
	}
	for _, line := range doc.Lines {
		if line.Romanized != "" {
			t.Fatalf("romanized %q with the switch off", line.Romanized)
		}
		for _, w := range line.Words {
			if w.Romanized != "" {
				t.Fatalf("word romanized %q with the switch off", w.Romanized)
			}
		}
	}
}

func TestLyricsRomanizationFillsBothTiers(t *testing.T) {
	root := isolateConfig(t)
	svc := newPlayerService()
	svc.player = newFakePlayer()

	media := lyricFixture(t, root,
		"[00:01.00]涙はそっと\n<涙:1.0:1.3|は:1.3:1.6|そっと:1.6:2.4>\n")
	svc.appSettings.Playback.Lyrics.AutoRomanization = true
	svc.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP

	doc, err := svc.Lyrics(media)
	if err != nil || doc == nil {
		t.Fatalf("Lyrics: doc %+v err %v", doc, err)
	}
	var line *lyrics.Line
	for i := range doc.Lines {
		if strings.Contains(doc.Lines[i].Text, "涙") {
			line = &doc.Lines[i]
		}
	}
	if line == nil {
		t.Fatalf("the fixture line is gone: %+v", doc.Lines)
	}
	if line.Text != "涙はそっと" {
		t.Fatalf("Text was rewritten to %q", line.Text)
	}
	if line.Romanized != "namidawasotto" {
		t.Fatalf("Romanized = %q, want namidawasotto", line.Romanized)
	}
	if len(line.Words) != 3 {
		t.Fatalf("words: %+v", line.Words)
	}
	at := 0
	for _, w := range line.Words {
		idx := strings.Index(line.Romanized[at:], w.Romanized)
		if idx < 0 {
			t.Fatalf("word %q is not a slice of %q after %d", w.Romanized, line.Romanized, at)
		}
		at += idx + len(w.Romanized)
	}
}

func TestLyricsRomanizationLeavesOtherLanguages(t *testing.T) {
	root := isolateConfig(t)
	svc := newPlayerService()
	svc.player = newFakePlayer()

	media := lyricFixture(t, root, "[00:01.00]涙はそっと\n")
	svc.appSettings.Playback.Lyrics.AutoRomanization = true
	svc.appSettings.Playback.Lyrics.RomanizationLanguage = "ko"

	doc, err := svc.Lyrics(media)
	if err != nil || doc == nil {
		t.Fatalf("Lyrics: doc %+v err %v", doc, err)
	}
	if doc.Lines[0].Romanized != "" {
		t.Fatalf("romanized %q for a language with no engine", doc.Lines[0].Romanized)
	}
}

func TestLyricsRomanizationSkipsLatinLines(t *testing.T) {
	root := isolateConfig(t)
	svc := newPlayerService()
	svc.player = newFakePlayer()

	media := lyricFixture(t, root, "[00:01.00]hello world\n[00:02.00]そら\n")
	svc.appSettings.Playback.Lyrics.AutoRomanization = true
	svc.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP

	doc, err := svc.Lyrics(media)
	if err != nil || doc == nil {
		t.Fatalf("Lyrics: doc %+v err %v", doc, err)
	}
	if doc.Lines[0].Romanized != "" {
		t.Fatalf("english line romanized to %q", doc.Lines[0].Romanized)
	}
	if doc.Lines[1].Romanized == "" {
		t.Fatalf("japanese line was not romanized: %+v", doc.Lines[1])
	}
}

func TestLyricsSpacingTogglesTheWordBreaks(t *testing.T) {
	root := isolateConfig(t)
	svc := newPlayerService()
	svc.player = newFakePlayer()

	media := lyricFixture(t, root,
		"[00:01.00]貴方が褒めてくれた\n<貴方:1.0:1.4|が:1.4:1.6|褒めて:1.6:2.0|くれた:2.0:2.6>\n")
	svc.appSettings.Playback.Lyrics.AutoRomanization = true
	svc.appSettings.Playback.Lyrics.RomanizationLanguage = lyricRomanizationJP

	read := func() *lyrics.Line {
		doc, err := svc.Lyrics(media)
		if err != nil || doc == nil {
			t.Fatalf("Lyrics: doc %+v err %v", doc, err)
		}
		for i := range doc.Lines {
			if strings.Contains(doc.Lines[i].Text, "貴方") {
				return &doc.Lines[i]
			}
		}
		t.Fatalf("the fixture line is gone: %+v", doc.Lines)

		return nil
	}

	svc.appSettings.Playback.Lyrics.AutoSpacing = false
	joined := read()
	if joined.Romanized != "anatagahometekureta" {
		t.Fatalf("spacing off: Romanized = %q", joined.Romanized)
	}

	svc.appSettings.Playback.Lyrics.AutoSpacing = true
	spaced := read()
	if spaced.Romanized != "anata ga home te kure ta" {
		t.Fatalf("spacing on: Romanized = %q", spaced.Romanized)
	}
	at := 0
	for _, w := range spaced.Words {
		idx := strings.Index(spaced.Romanized[at:], w.Romanized)
		if idx < 0 {
			t.Fatalf("word %q is not a slice of %q after %d", w.Romanized, spaced.Romanized, at)
		}
		at += idx + len(w.Romanized)
	}
}

func TestLyricsSettingsRomanizationNormalization(t *testing.T) {
	got := normalizeLyricsSettings(LyricsSettings{RomanizationLanguage: "tlh"}, defaultLyricsSettings())
	if got.RomanizationLanguage != lyricRomanizationJP {
		t.Fatalf("language = %q, want %q", got.RomanizationLanguage, lyricRomanizationJP)
	}

	// Pre-feature settings JSON omits the new fields.
	var old LyricsSettings
	if err := json.Unmarshal([]byte(`{"providers":{},"saveLocation":"lyrics","folder":""}`), &old); err != nil {
		t.Fatal(err)
	}
	got = normalizeLyricsSettings(old, defaultLyricsSettings())
	if got.AutoRomanization {
		t.Fatal("a pre-feature file turned romanization on")
	}
	if got.AutoSpacing {
		t.Fatal("a pre-feature file turned auto spacing on")
	}
	if got.RomanizationLanguage != lyricRomanizationJP {
		t.Fatalf("language = %q, want %q", got.RomanizationLanguage, lyricRomanizationJP)
	}
	if got.WordSweep != lyricWordSweepBoth {
		t.Fatalf("word sweep = %q, want %q", got.WordSweep, lyricWordSweepBoth)
	}

	got = normalizeLyricsSettings(LyricsSettings{WordSweep: "romaji"}, defaultLyricsSettings())
	if got.WordSweep != lyricWordSweepBoth {
		t.Fatalf("unknown sweep = %q, want %q", got.WordSweep, lyricWordSweepBoth)
	}

	saved := normalizeLyricsSettings(LyricsSettings{
		AutoRomanization:     true,
		RomanizationLanguage: "jp",
		AutoSpacing:          true,
		WordSweep:            lyricWordSweepOriginal,
	}, defaultLyricsSettings())
	if !saved.AutoRomanization || saved.RomanizationLanguage != "jp" || !saved.AutoSpacing {
		t.Fatalf("saved = %+v", saved)
	}
	if saved.WordSweep != lyricWordSweepOriginal {
		t.Fatalf("saved sweep = %q, want %q", saved.WordSweep, lyricWordSweepOriginal)
	}
}
