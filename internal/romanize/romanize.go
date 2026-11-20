// Package romanize converts Japanese lyric lines to Hepburn romaji using kagome
// and IPADIC. Lines are converted whole before cutting at timed word spans so
// particles and compounds keep their sentence reading.
package romanize

import (
	"fmt"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/ikawaha/kagome-dict/dict"
	"github.com/ikawaha/kagome-dict/ipa"
	"github.com/ikawaha/kagome/v2/tokenizer"
)

// Word is one timed span of a line; its text must appear in order in the line.
type Word struct {
	Text string
}

// Result is a romanized line and its per-word slices. Every Words entry is a
// contiguous in-order slice of Line; when OK is false a word missed and Line is
// still populated.
type Result struct {
	Line  string
	Words []string
	OK    bool
}

// Options controls romaji formatting without changing readings.
type Options struct {
	// Spacing inserts spaces between morphemes while preserving single source
	// spaces, joining っ to the next morpheme, and keeping punctuation attached.
	Spacing bool
}

// Engine tokenizes Japanese text with an IPADIC dictionary. [New] uses the
// package-pinned [ipa.Dict]; [Open] loads an owned file that [Engine.Close] can
// reclaim.
type Engine struct {
	tk atomic.Pointer[tokenizer.Tokenizer]
}

// New builds an engine over the embedded [ipa.Dict], which stays pinned for the
// process lifetime. Prefer [Open] with [WriteCache] when memory must be freed.
func New() *Engine {
	tk, err := tokenizer.New(ipa.Dict(), tokenizer.OmitBosEos())
	if err != nil {
		// Embedded dictionary cannot be repaired at runtime.
		panic("romanize: build tokenizer: " + err.Error())
	}
	e := &Engine{}
	e.tk.Store(tk)

	return e
}

// Open builds an engine over a dictionary file such as one written by
// [WriteCache], so [Engine.Close] can free the parsed tables.
func Open(path string) (*Engine, error) {
	d, err := dict.LoadDictFile(path)
	if err != nil {
		return nil, fmt.Errorf("romanize: load dictionary: %w", err)
	}
	tk, err := tokenizer.New(d, tokenizer.OmitBosEos())
	if err != nil {
		return nil, fmt.Errorf("romanize: build tokenizer: %w", err)
	}
	e := &Engine{}
	e.tk.Store(tk)

	return e, nil
}

// Close releases the tokenizer so the parsed dictionary can be garbage-collected.
// Safe to call concurrently with Romanize; later calls return an empty Result.
func (e *Engine) Close() {
	if e == nil {
		return
	}
	e.tk.Store(nil)
}

// unit is one morpheme in rune offsets; read holds pronunciation when present
// so particles like は convert as wa.
type unit struct {
	start, end    int
	surface, read string
	romaji        string
}

// IPA feature indices; Token.Reading omits pronunciation and meta-key lookups.
const (
	featureReading = ipa.Reading
	featurePron    = ipa.Pronunciation
)

// Romanize converts text and cuts the result at the timed word spans.
func (e *Engine) Romanize(text string, words []Word) Result {
	return e.RomanizeWith(text, words, Options{})
}

// RomanizeWith converts text with presentation options applied to both Line and
// Words so the sweep contract still holds.
func (e *Engine) RomanizeWith(text string, words []Word, opts Options) Result {
	if e == nil {
		return Result{}
	}
	tk := e.tk.Load()
	if tk == nil {
		return Result{}
	}
	src := []rune(text)
	units := buildUnits(tk, text)
	line := assemble(src, units, opts.Spacing)

	spans, ok := locate(src, words)
	if !ok {
		return Result{Line: line, OK: false}
	}
	out := make([]string, len(spans))
	for i, sp := range spans {
		out[i] = cut(units, sp[0], sp[1], opts.Spacing)
	}

	return Result{Line: line, Words: out, OK: true}
}

// buildUnits tokenizes text and converts all readings in one pass so っ and ー
// carry across morpheme boundaries.
func buildUnits(tk *tokenizer.Tokenizer, text string) []unit {
	tokens := tk.Tokenize(text)
	starts := make([]int, len(tokens))
	readings := make([]string, len(tokens))
	at := 0
	for i, t := range tokens {
		readings[i] = readingOf(t)
		starts[i] = at
		at += len([]rune(t.Surface))
	}
	romajis := Parts(readings)
	out := make([]unit, len(tokens))
	for i := range tokens {
		end := at
		if i+1 < len(tokens) {
			end = starts[i+1]
		}
		out[i] = unit{
			start:   starts[i],
			end:     end,
			surface: tokens[i].Surface,
			read:    readings[i],
			romaji:  romajis[i],
		}
	}

	return out
}

// readingOf prefers pronunciation over reading and falls back to surface for
// latin, punctuation, and unknown kanji.
func readingOf(t tokenizer.Token) string {
	pick := func(i int) string {
		v, ok := t.FeatureAt(i)
		if !ok || v == "" || v == "*" {
			return ""
		}

		return v
	}
	if v := pick(featurePron); v != "" {
		return v
	}
	if v := pick(featureReading); v != "" {
		return v
	}

	return t.Surface
}

// assemble joins converted units and copies any uncovered source runes verbatim.
func assemble(src []rune, units []unit, spacing bool) string {
	var b strings.Builder
	at := 0
	for i, u := range units {
		if u.start > at && u.start <= len(src) {
			b.WriteString(string(src[at:u.start]))
		}
		if i > 0 && separates(units[i-1], u, spacing) {
			b.WriteByte(' ')
		}
		b.WriteString(u.piece(spacing))
		at = u.end
	}
	if at < len(src) {
		b.WriteString(string(src[at:]))
	}

	return b.String()
}

// piece omits whitespace tokens when spacing is on because separates emits the
// break instead.
func (u unit) piece(spacing bool) string {
	if spacing && isBlank(u) {
		return ""
	}

	return u.romaji
}

func isBlank(u unit) bool {
	return u.surface != "" && strings.TrimSpace(u.surface) == ""
}

// separates is shared by assemble and cut so line and word spacing stay aligned.
// Blank tokens defer one space to the next token; っ and adjacent punctuation
// suppress it.
func separates(prev, cur unit, spacing bool) bool {
	if !spacing {
		return false
	}
	if isBlank(cur) {
		return false
	}
	if isBlank(prev) {
		return true
	}
	if prev.romaji == "" || cur.romaji == "" || endsOnSokuon(prev.read) {
		return false
	}

	return !hugs(prev.romaji, cur.romaji)
}

func endsOnSokuon(read string) bool {
	r := []rune(read)
	if len(r) == 0 {
		return false
	}

	return katakanaRune(r[len(r)-1]) == 'っ'
}

// Punctuation that attaches to its neighbor without an inserted space.
const (
	noSpaceBefore = "、。，．・：；？！）］｝」』】〉》〕｣,.;:!?)]}"
	noSpaceAfter  = "（［｛「『【〈《〔｢([{"
)

func hugs(prev, cur string) bool {
	last := []rune(prev)
	first := []rune(cur)
	if len(last) == 0 || len(first) == 0 {
		return true
	}

	return strings.ContainsRune(noSpaceBefore, first[0]) ||
		strings.ContainsRune(noSpaceAfter, last[len(last)-1])
}

// locate returns in-order rune ranges for words, advancing past each match so
// repeated words never alias an earlier occurrence.
func locate(src []rune, words []Word) ([][2]int, bool) {
	spans := make([][2]int, len(words))
	at := 0
	for i, w := range words {
		span := []rune(w.Text)
		if len(span) == 0 {
			spans[i] = [2]int{at, at}
			continue
		}
		idx := indexRunes(src, span, at)
		if idx < 0 {
			return nil, false
		}
		spans[i] = [2]int{idx, idx + len(span)}
		at = idx + len(span)
	}

	return spans, true
}

func indexRunes(src, want []rune, from int) int {
	for i := from; i+len(want) <= len(src); i++ {
		if runesEqual(src[i:i+len(want)], want) {
			return i
		}
	}

	return -1
}

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// cut extracts the romaji slice for [from, to), applying separates only between
// morphemes inside the span so leading gaps stay outside the word.
func cut(units []unit, from, to int, spacing bool) string {
	var (
		b    strings.Builder
		prev *unit
	)
	for i := range units {
		u := &units[i]
		if u.end <= from || u.start >= to {
			continue
		}
		if prev != nil && separates(*prev, *u, spacing) && b.Len() > 0 {
			b.WriteByte(' ')
		}
		prev = u
		span := u.end - u.start
		if span <= 0 {
			continue
		}
		if spacing && isBlank(*u) {
			continue
		}
		if pieces, ok := runePieces(*u); ok {
			lo := max(0, from-u.start)
			hi := min(span, to-u.start)
			for _, p := range pieces[lo:hi] {
				b.WriteString(p)
			}
			continue
		}
		// Proportional fallback when a kanji head has no rune-level alignment.
		r := []rune(u.romaji)
		lo := 0
		if from > u.start {
			lo = len(r) * (from - u.start) / span
		}
		hi := len(r)
		if to < u.end {
			hi = len(r) * (to - u.start) / span
		}
		if lo < hi {
			b.WriteString(string(r[lo:hi]))
		}
	}

	return b.String()
}

// runePieces aligns a morpheme's romaji one-to-one with its surface runes for
// kana-only words and single-kanji okurigana (e.g. 砕|け -> kuda|ke).
func runePieces(u unit) ([]string, bool) {
	src := []rune(u.surface)
	read := []rune(u.read)
	if len(src) == 0 || len(src) != u.end-u.start || len(read) == 0 {
		return nil, false
	}

	var pieces []string
	if len(read) == len(src) {
		for _, r := range read {
			if !isKana(r) {
				return nil, false
			}
			pieces = append(pieces, string(r))
		}
		pieces = Parts(pieces)
		// Hand a digraph's trailing char to its small kana so per-char sweeps stay non-empty.
		for i := 1; i < len(pieces); i++ {
			if pieces[i] != "" {
				continue
			}
			if prev := []rune(pieces[i-1]); len(prev) > 1 {
				pieces[i] = string(prev[len(prev)-1:])
				pieces[i-1] = string(prev[:len(prev)-1])
			}
		}
	} else {
		// Match trailing okurigana; stop before っ/ッ since doubling depends on the next rune.
		tail := 0
		for tail < len(src) && tail < len(read) {
			s := src[len(src)-1-tail]
			if !isKana(s) || katakanaRune(s) == 'っ' || katakanaRune(s) != katakanaRune(read[len(read)-1-tail]) {
				break
			}
			tail++
		}
		head := len(src) - tail
		// Multi-kanji heads share one reading and fall back to proportional cuts.
		if head != 1 || tail >= len(read) {
			return nil, false
		}
		headRomaji := Parts([]string{string(read[:len(read)-tail])})
		if len(headRomaji) != 1 || headRomaji[0] == "" {
			return nil, false
		}
		tailRunes := make([]string, 0, tail)
		for _, r := range read[len(read)-tail:] {
			tailRunes = append(tailRunes, string(r))
		}
		pieces = append([]string{headRomaji[0]}, Parts(tailRunes)...)
	}

	if len(pieces) != len(src) || strings.Join(pieces, "") != u.romaji {
		return nil, false
	}

	return pieces, true
}

// HasJapanese reports whether s contains any hiragana, katakana, or kanji.
func HasJapanese(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Han) {
			return true
		}
	}

	return false
}
