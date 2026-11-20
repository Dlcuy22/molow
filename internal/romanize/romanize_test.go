package romanize

import (
	"strings"
	"testing"
)

// lineWords marks the timed spans of a fixture line with the same syntax the
// sheet uses, "surface:start:end", so a test reads like the source it mirrors.
func lineWords(spec string) []Word {
	if spec == "" {
		return nil
	}
	fields := strings.Split(spec, "|")
	out := make([]Word, 0, len(fields))
	for _, f := range fields {
		i := strings.LastIndex(f, ":")
		if i < 0 {
			out = append(out, Word{Text: f})
			continue
		}
		out = append(out, Word{Text: f[:i]})

		_ = i
	}

	return out
}

func TestRomanizeLine(t *testing.T) {
	e := New()
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "kanji takes its reading", text: "涙はそっと枯れていく", want: "namidawasottokareteiku"},
		{name: "particle は is sung wa", text: "僕は歌う", want: "bokuwautau"},
		{name: "particle へ is sung e", text: "家へ帰る", want: "ieekaeru"},
		{name: "katakana folds", text: "ヴァンパイア", want: "vampaia"},
		{name: "unknown kanji stays kanji", text: "可惜", want: "ka惜"},
		{name: "latin passes through", text: "hello world", want: "hello world"},
		{name: "punctuation passes through", text: "だって = あいたく", want: "datte = aitaku"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := e.Romanize(tc.text, nil)
			if got.Line != tc.want {
				t.Fatalf("Romanize(%q).Line = %q, want %q", tc.text, got.Line, tc.want)
			}
		})
	}
}

func TestWordSpansAreSubstrings(t *testing.T) {
	e := New()
	lines := []struct {
		text  string
		words []Word
	}{
		{
			text:  "ゆらゆらり",
			words: []Word{{Text: "ゆら"}, {Text: "ゆらり"}},
		},
		{
			text:  "あたしヴァンパイア",
			words: []Word{{Text: "あ"}, {Text: "た"}, {Text: "し"}, {Text: "ヴァンパイア"}},
		},
		{
			text:  "涙はそっと枯れていく",
			words: []Word{{Text: "涙"}, {Text: "は"}, {Text: "そ"}, {Text: "っ"}, {Text: "と"}, {Text: "枯れ"}, {Text: "て"}, {Text: "いく"}},
		},
		{
			text:  "可笑しいよねって笑ってる",
			words: []Word{{Text: "可笑"}, {Text: "しい"}, {Text: "よね"}, {Text: "って"}, {Text: "笑ってる"}},
		},
	}

	for _, tc := range lines {
		got := e.Romanize(tc.text, tc.words)
		if !got.OK {
			t.Fatalf("Romanize(%q) could not place a word", tc.text)
		}
		if len(got.Words) != len(tc.words) {
			t.Fatalf("Romanize(%q) returned %d words, want %d", tc.text, len(got.Words), len(tc.words))
		}
		at := 0
		for i, w := range got.Words {
			idx := strings.Index(got.Line[at:], w)
			if idx < 0 {
				t.Fatalf("Romanize(%q): word %d %q not found in %q after offset %d", tc.text, i, w, got.Line, at)
			}
			at += idx + len(w)
		}
	}
}

func TestRomanizeMissingWordIsNotOK(t *testing.T) {
	e := New()
	got := e.Romanize("ゆらゆらり", []Word{{Text: "そっと"}})
	if got.OK {
		t.Fatal("Romanize reported OK for a word the line does not contain")
	}
	if got.Line == "" {
		t.Fatal("Romanize dropped the line on a miss, want it still converted")
	}
}

func TestMoraCut(t *testing.T) {
	e := New()
	got := e.Romanize("そっと", []Word{{Text: "そ"}, {Text: "っ"}, {Text: "と"}})
	if !got.OK {
		t.Fatal("Romanize could not place a word")
	}
	want := []string{"so", "t", "to"}
	for i := range want {
		if got.Words[i] != want[i] {
			t.Fatalf("word %d = %q, want %q (all: %q)", i, got.Words[i], want[i], got.Words)
		}
	}
}

func TestOkuriganaCut(t *testing.T) {
	e := New()
	got := e.Romanize("砕けて", []Word{{Text: "砕"}, {Text: "け"}, {Text: "て"}})
	if !got.OK {
		t.Fatal("Romanize could not place a word")
	}
	if got.Line != "kudakete" {
		t.Fatalf("line = %q, want kudakete", got.Line)
	}
	want := []string{"kuda", "ke", "te"}
	for i := range want {
		if got.Words[i] != want[i] {
			t.Fatalf("word %d = %q, want %q (all: %q)", i, got.Words[i], want[i], got.Words)
		}
	}
}

func TestHasJapanese(t *testing.T) {
	for _, s := range []string{"涙", "かな", "カナ", "a涙b"} {
		if !HasJapanese(s) {
			t.Fatalf("HasJapanese(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "hello", "123 !", "namida"} {
		if HasJapanese(s) {
			t.Fatalf("HasJapanese(%q) = true, want false", s)
		}
	}
}

func TestSpacing(t *testing.T) {
	e := New()
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "one space per morpheme", text: "貴方が褒めてくれたこの翼を", want: "anata ga home te kure ta kono tsubasa o"},
		{name: "particle stands alone", text: "泥の分際で", want: "doro no bunzai de"},
		{name: "a source space is not doubled", text: "本当に愛してる 私だけの", want: "hontooni aishi teru watashi dake no"},
		{name: "a run of source spaces collapses", text: "未練 未練 未練しか", want: "miren miren miren shika"},
		{name: "brackets hug their word", text: "「ごめんなさい」すらも", want: "「gomennasai」 sura mo"},
		{name: "punctuation hugs its word", text: "hello, world!", want: "hello, world!"},
		{name: "a sokuon joins the next morpheme", text: "待って", want: "matte"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := e.RomanizeWith(tc.text, nil, Options{Spacing: true}).Line
			if got != tc.want {
				t.Fatalf("spaced(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestSpacingKeepsTheSweep(t *testing.T) {
	e := New()
	lines := []struct {
		text  string
		words []Word
	}{
		{
			text:  "本当に愛してる 私だけの貴方を",
			words: []Word{{Text: "本当に"}, {Text: "愛してる"}, {Text: " "}, {Text: "私"}, {Text: "だけ"}, {Text: "の"}, {Text: "貴方"}, {Text: "を"}},
		},
		{
			text:  "涙はそっと枯れていく",
			words: []Word{{Text: "涙"}, {Text: "は"}, {Text: "そ"}, {Text: "っ"}, {Text: "と"}, {Text: "枯れ"}, {Text: "て"}, {Text: "いく"}},
		},
		{
			text:  "可笑しいよねって笑ってる",
			words: []Word{{Text: "可笑"}, {Text: "しい"}, {Text: "よね"}, {Text: "って"}, {Text: "笑ってる"}},
		},
		{
			text:  "未練 未練 未練しか",
			words: []Word{{Text: "未練"}, {Text: " "}, {Text: "未練"}, {Text: " "}, {Text: "未練"}, {Text: "しか"}},
		},
	}
	for _, tc := range lines {
		got := e.RomanizeWith(tc.text, tc.words, Options{Spacing: true})
		if !got.OK {
			t.Fatalf("spaced Romanize(%q) could not place a word", tc.text)
		}
		at := 0
		for i, w := range got.Words {
			idx := strings.Index(got.Line[at:], w)
			if idx < 0 {
				t.Fatalf("spaced Romanize(%q): word %d %q not found in %q after %d (all: %q)", tc.text, i, w, got.Line, at, got.Words)
			}
			at += idx + len(w)
		}
	}
}

func TestSpacingJoinsTheUnspacedLine(t *testing.T) {
	e := New()
	for _, text := range []string{"貴方が褒めてくれたこの翼を", "本当に愛してる 私だけの", "涙はそっと枯れていく", "hello, world!"} {
		joined := e.Romanize(text, nil).Line
		spaced := e.RomanizeWith(text, nil, Options{Spacing: true}).Line
		if got := strings.ReplaceAll(spaced, " ", ""); got != strings.ReplaceAll(joined, " ", "") {
			t.Fatalf("spaced(%q) = %q does not spell the same as %q", text, spaced, joined)
		}
	}
}
