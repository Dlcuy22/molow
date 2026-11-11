// Package lyrics finds, fetches and parses the lyric sheet for a track.
//
// The lookup is deliberately forgiving: albums are tagged by hand and a lyric
// file rarely matches the audio name byte for byte. A file is accepted when its
// name equals the track's, equals it after a leading track number is removed,
// or equals it plus a language suffix such as ".en" or ".trans".
//
// [Fetch] runs the provider chain when no sidecar exists, and [Parse] and
// [Serialize] read and write the one canonical format both paths share.
package lyrics

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Line is one lyric line and its translation, keyed by the timestamp they
// share. A file with no translation leaves Translation empty. Words carries
// the per-word timing of a word-synced line; a line-synced line leaves it nil.
type Line struct {
	TimeMs      int64  `json:"timeMs"`
	Text        string `json:"text"`
	Translation string `json:"translation"`
	Words       []Word `json:"words,omitempty"`
}

// Word is one sung segment of a line and its timing. End is always set: a
// provider may not supply one, so it falls back to the next word's start and
// for the last word to the next line's start, or half a second.
type Word struct {
	Text    string `json:"text"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

// Document is what the player draws. Synced is false for a plain-text lyric
// file, which the player shows without following the position.
type Document struct {
	Lines  []Line `json:"lines"`
	Synced bool   `json:"synced"`
	Source string `json:"source"`
}

// Load finds the lyric file for mediaPath and parses it. A missing file is
// reported as (nil, nil): no lyrics is a normal state, not an engine error.
// extraDirs are additional directories to search, ahead of the ones the track
// itself suggests; a localStorage-less reference such as a YouTube Music track
// uses them to hold its videoId file.
func Load(mediaPath string, extraDirs ...string) (*Document, error) {
	main, translation := find(mediaPath, extraDirs)
	if main == "" {
		return nil, nil
	}

	lines, synced, err := parse(main)
	if err != nil {
		return nil, err
	}
	if translation != "" {
		if extra, _, err := parse(translation); err == nil {
			mergeTranslation(lines, extra)
		}
	}
	lines = pairDuplicateTimes(lines)

	return &Document{Lines: lines, Synced: synced, Source: main}, nil
}

// LoadRef is the local-file lookup for a reference the music folders do not
// hold, a cached YouTube Music track. The file is named <id>.lrc and lives in
// one of the extra directories. The source line lets a caller recognise it the
// way Load does.
func LoadRef(id string, dirs []string) (*Document, error) {
	main := findByID(id, dirs)
	if main == "" {
		return nil, nil
	}

	lines, synced, err := parse(main)
	if err != nil {
		return nil, err
	}
	lines = pairDuplicateTimes(lines)

	return &Document{Lines: lines, Synced: synced, Source: main}, nil
}

// Write stores raw lyric text at path, atomically. Sidecar writes go through
// the same temp-and-rename discipline as every other small file the app
// persists, so a crash cannot leave a half file a later read would choke on.
func Write(path, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return os.ErrInvalid
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".lrc-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	if _, err := f.WriteString(raw + "\n"); err != nil {
		f.Close()
		os.Remove(name)

		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(name)

		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)

		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)

		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)

		return err
	}

	return nil
}

var (
	timestamp = regexp.MustCompile(`\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)
	// wordTagTime matches one inline rich-sync timestamp, `<02:18.07>`.
	wordTagTime = regexp.MustCompile(`<(\d{1,2}):(\d{2})(?:[.:](\d{1,3}))?>`)
	// wordBlockLine is a standalone continuation line the word block arrived
	// on, `<just:4.23:4.6|a:4.6:4.71>`: angles, no square bracket, word and two
	// number fields.
	wordBlockLine = regexp.MustCompile(`(?m)^<[^>\[\]]*[:|][^>\[\]]*>$`)
)

// parse reads the lyric file at path. It is the disk half of Parse.
func parse(path string) ([]Line, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}

	return Parse(string(raw))
}

// Parse turns one canonical LRC document into lines. The format is the sidecar
// LRC plus the two word-synced extensions a fetched sheet can carry: a word
// block on its own line, attached to the timestamped line above it, and an
// inline rich sync where every word is preceded by its own timestamp. A line
// without a timestamp is kept as unsynced content.
func Parse(src string) ([]Line, bool, error) {
	var (
		out    []Line
		synced bool
	)
	blocks := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for _, block := range blocks {
		raw := strings.TrimSpace(block)
		if raw == "" {
			continue
		}
		locs := timestamp.FindAllStringSubmatchIndex(raw, -1)
		if len(locs) == 0 {
			// Metadata such as [ar:Artist] and a genuinely untimed line both
			// land here; a leading bracket with a colon is always metadata.
			if strings.HasPrefix(raw, "[") && strings.Contains(raw[:min(len(raw), 6)], ":") {
				continue
			}
			// A word block, `<word:start:end|...>`, belongs to the timestamped
			// line above it. Inline rich sync, `<mm:ss.xx>word ...`, carries
			// its own time tags.
			switch {
			case wordBlockLine.MatchString(raw) && len(out) > 0:
				words, ok := parseWordBlock(raw)
				if ok {
					i := len(out) - 1
					out[i].Words = words
					continue
				}
			case len(wordTagTime.FindStringSubmatch(raw)) > 0:
				line, ok := parseRichSync(raw)
				if ok {
					out = append(out, line)
					synced = true
					continue
				}
			}
			out = append(out, Line{TimeMs: -1, Text: raw})
			continue
		}
		text := strings.TrimSpace(timestamp.ReplaceAllString(raw, ""))
		if text == "" {
			continue
		}
		if match := wordTagTime.FindString(text); match != "" {
			// Rich sync hidden behind a line timestamp: the per-word tags carry
			// the timing and the outer one only marks the line start.
			line, ok := parseRichSync(text)
			if ok {
				first := locs[0]
				line.TimeMs = parseTime(raw[first[2]:first[3]], raw[first[4]:first[5]], raw[first[6]:first[7]])
				out = append(out, line)
				synced = true
				continue
			}
		}
		for _, loc := range locs {
			ms := parseTime(raw[loc[2]:loc[3]], raw[loc[4]:loc[5]], raw[loc[6]:loc[7]])
			out = append(out, Line{TimeMs: ms, Text: text})
			synced = true
		}
	}
	if len(out) > 0 && synced {
		setWordEnds(out)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TimeMs < out[j].TimeMs })

	return out, synced, nil
}

// parseWordBlock reads the continuation format of the providers:
// `<text:startSec:endSec|text:...>`. Text keeps embedded spaces, so the fields
// are cut from the right. Seconds carry decimals.
func parseWordBlock(raw string) ([]Word, bool) {
	segments := strings.Split(strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">"), "|")
	words := make([]Word, 0, len(segments))
	for _, seg := range segments {
		// Fields are numeric and read from the right, so a text carrying a
		// colon, "Won't: Stop", keeps its text whole.
		i2 := strings.LastIndex(seg, ":")
		if i2 < 0 {
			return nil, false
		}
		endMs, ok := parseSeconds(seg[i2+1:])
		if !ok {
			return nil, false
		}
		i1 := strings.LastIndex(seg[:i2], ":")
		if i1 < 0 {
			return nil, false
		}
		startMs, ok := parseSeconds(seg[i1+1 : i2])
		if !ok {
			return nil, false
		}
		words = append(words, Word{Text: strings.TrimSpace(seg[:i1]), StartMs: startMs, EndMs: endMs})
	}

	return words, len(words) > 0
}

// parseSeconds reads a fractional seconds field, as the block format writes it.
func parseSeconds(s string) (int64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 {
		return 0, false
	}

	return int64(v*1000 + 0.5), true
}

// joinWordTexts rebuilds a line's text from its words. The space goes between
// two Latin words and nowhere else, since a plain " " would split Japanese back
// into "あの ね".
func joinWordTexts(ws []Word) string {
	var b strings.Builder
	for i, w := range ws {
		if i > 0 && endsLatin(ws[i-1].Text) && startsLatin(w.Text) {
			b.WriteByte(' ')
		}
		b.WriteString(w.Text)
	}

	return strings.TrimSpace(b.String())
}

// endsLatin reports whether the trailing character is an ASCII letter or
// digit, ignoring trailing whitespace, which already separates the words.
func endsLatin(s string) bool {
	s = strings.TrimRight(s, " \t")
	if s == "" {
		return false
	}
	last := s[len(s)-1]

	return last >= '0' && last <= '9' ||
		(last >= 'a' && last <= 'z') ||
		(last >= 'A' && last <= 'Z')
}

// startsLatin reports whether the leading character is an ASCII letter or
// digit.
func startsLatin(s string) bool {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return false
	}
	first := s[0]

	return first >= '0' && first <= '9' ||
		(first >= 'a' && first <= 'z') ||
		(first >= 'A' && first <= 'Z')
}

// parseRichSync reads the inline format, `<mm:ss.xx>word ...`: the timestamps
// split the text into words whose ends only the next tag knows.
func parseRichSync(text string) (Line, bool) {
	locs := wordTagTime.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return Line{}, false
	}
	first := wordTagTime.FindStringSubmatch(text)

	// Each tag precedes its word: the word runs from this tag to the next,
	// and a tag with nothing after it closes the previous word instead.
	line := Line{TimeMs: parseTime(first[1], first[2], first[3])}
	for i, loc := range locs {
		to := len(text)
		if i+1 < len(locs) {
			to = locs[i+1][0]
		}
		startMs := parseTime(text[loc[2]:loc[3]], text[loc[4]:loc[5]], text[loc[6]:loc[7]])
		seg := strings.TrimSpace(text[loc[1]:to])
		if seg == "" {
			if n := len(line.Words); n > 0 {
				line.Words[n-1].EndMs = startMs
			}
			continue
		}
		line.Words = append(line.Words, Word{Text: seg, StartMs: startMs, EndMs: startMs})
	}
	if len(line.Words) == 0 {
		return Line{}, false
	}
	line.Text = joinWordTexts(line.Words)

	return line, true
}

// setWordEnds fills in the ends a rich sync leaves open: a word ends where the
// next one begins, and the last word where the next line begins, or half a
// second later when there is no next line. Word blocks carry their own ends.
func setWordEnds(lines []Line) {
	nextLine := make([]int64, len(lines))
	for i := range lines {
		var next int64
		if i+1 < len(lines) && lines[i+1].TimeMs >= 0 {
			next = lines[i+1].TimeMs
		}
		nextLine[i] = next
	}
	for i := range lines {
		ws := lines[i].Words
		for j := range ws {
			if ws[j].EndMs > ws[j].StartMs || ws[j].EndMs < 0 {
				continue
			}
			if j+1 < len(ws) {
				ws[j].EndMs = ws[j+1].StartMs
				continue
			}
			if nextLine[i] > ws[j].StartMs {
				ws[j].EndMs = nextLine[i]
				continue
			}
			ws[j].EndMs = ws[j].StartMs + 500
		}
	}
}

func parseTime(mm, ss, frac string) int64 {
	m, _ := strconv.ParseInt(mm, 10, 64)
	s, _ := strconv.ParseInt(ss, 10, 64)
	ms := m*60_000 + s*1000
	if frac != "" {
		// A one- or two-digit fraction is hundredths; three is milliseconds.
		switch len(frac) {
		case 1:
			v, _ := strconv.ParseInt(frac, 10, 64)
			ms += v * 100
		case 2:
			v, _ := strconv.ParseInt(frac, 10, 64)
			ms += v * 10
		default:
			v, _ := strconv.ParseInt(frac[:3], 10, 64)
			ms += v
		}
	}

	return ms
}

// pairDuplicateTimes fills Translation from a second line at the same time,
// which is how one bilingual LRC usually stores a pair.
func pairDuplicateTimes(lines []Line) []Line {
	out := make([]Line, 0, len(lines))
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j].TimeMs == lines[i].TimeMs {
			j++
		}
		primary := lines[i]
		for _, l := range lines[i+1 : j] {
			if l.Text != "" && l.Text != primary.Text {
				primary.Translation = l.Text

				break
			}
		}
		out = append(out, primary)
		i = j
	}

	return out
}

// mergeTranslation attaches a translation file by timestamp.
func mergeTranslation(main, extra []Line) {
	if len(main) == 0 || len(extra) == 0 {
		return
	}
	byTime := make(map[int64]string, len(extra))
	for _, l := range extra {
		byTime[l.TimeMs] = l.Text
	}
	for i := range main {
		if main[i].Translation != "" {
			continue
		}
		if text, ok := byTime[main[i].TimeMs]; ok {
			main[i].Translation = text
		}
	}
}

var languageSuffix = map[string]bool{
	"en": true, "eng": true, "trans": true, "translation": true,
	"romaji": true, "romaja": true, "zh": true, "vi": true, "id": true,
}

// find returns the main and translation lyric paths, either of which may be
// empty.
func find(mediaPath string, extraDirs []string) (string, string) {
	dir := filepath.Dir(mediaPath)
	base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	key := normalize(base)

	dirs := append(append([]string{}, extraDirs...), dir, filepath.Join(dir, "lyrics"))
	var (
		main                 string
		translation          string
		lastMainMatch        int
		lastTranslationMatch int
	)
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".lrc") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			lang := ""
			stem := name
			if i := strings.LastIndex(name, "."); i > 0 {
				candidate := strings.ToLower(name[i+1:])
				if languageSuffix[candidate] {
					lang = candidate
					stem = name[:i]
				}
			}
			// The cleaned tiers only fill an empty slot: a cleaned name
			// ("Song (Official MV).lrc" beside "Song.flac"), then the
			// "Artist - Title" half ("Billie Jean.lrc" beside
			// "Michael Jackson - Billie Jean.opus").
			match := matchScore(base, key, stem)
			if match == 0 {
				match = cleanedScore(base, key, stem)
			}
			if match == 0 {
				match = titleScore(base, stem)
			}
			if match == 0 {
				continue
			}
			path := filepath.Join(d, e.Name())
			if lang != "" {
				if translation == "" || (match > 1 && match > lastTranslationMatch) {
					translation = path
					lastTranslationMatch = match
				}
				continue
			}
			if main == "" || (match > 1 && match > lastMainMatch) {
				main = path
				lastMainMatch = match
			}
		}
	}

	return main, translation
}

// findByID is the lookup a catalogue reference uses: a file named <id>.lrc in
// one of the extra directories, the save location the fetched sheets land in.
func findByID(id string, dirs []string) string {
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".lrc") {
				continue
			}
			if strings.EqualFold(strings.TrimSuffix(e.Name(), ".lrc"), id) {
				return filepath.Join(d, e.Name())
			}
		}
	}

	return ""
}

// IDPath returns the sidecar path a reference already has on disk, or "" when
// there is none. The prefetch consults it instead of the whole file parse.
func IDPath(id string, dirs []string) string {
	return findByID(id, dirs)
}

// matchScore is 2 for an exact stem, 1 for a normalized match, 0 for none.
func matchScore(base, key, stem string) int {
	if stem == base {
		return 2
	}
	if normalize(stem) == key {
		return 1
	}

	return 0
}

// cleanedScore is 1 when stem and track name agree once the marketing suffixes
// are removed from both, 0 otherwise. It is the weakest tier of find's
// matching and can only fill an empty slot, never displace an exact stem.
func cleanedScore(base, key, stem string) int {
	if normalize(CleanKey(base)) != "" && normalize(CleanKey(base)) == normalize(CleanKey(stem)) {
		return 1
	}

	return 0
}

// dashTail splits an "Artist - Title" name at the last separator, ok is false
// when there is none and s itself is returned.
func dashTail(s string) (string, bool) {
	if i := strings.LastIndex(s, " - "); i >= 0 {
		return strings.TrimSpace(s[i+3:]), true
	}

	return s, false
}

// titleScore is 1 when the title half of an "Artist - Title" name matches the
// other side, cleaned of junk suffixes. It joins a titled track file and a
// bare-title sidecar, either way around. Same weakest tier as cleanedScore.
func titleScore(base, stem string) int {
	baseTitle, _ := dashTail(base)
	stemTitle, _ := dashTail(stem)
	baseKey := normalize(CleanKey(baseTitle))
	stemKey := normalize(CleanKey(stemTitle))
	if baseKey == "" || stemKey == "" || baseKey != stemKey {
		return 0
	}

	return 1
}

var leadingIndex = regexp.MustCompile(`^\s*(?:cd\s*)?\d{1,3}\s*[.\-_ ]\s*`)

// normalize removes the track index, punctuation and case so "01. Song" and
// "Song" compare equal.
func normalize(s string) string {
	s = leadingIndex.ReplaceAllString(s, "")
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '_', '-', '.', '\'', '’':
			continue
		}
		b.WriteRune(r)
	}

	return b.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}

	return b
}
