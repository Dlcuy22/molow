// Package lyrics finds and parses the sidecar lyric file for a track.
//
// The lookup is deliberately forgiving: albums are tagged by hand and a lyric
// file rarely matches the audio name byte for byte. A file is accepted when its
// name equals the track's, equals it after a leading track number is removed,
// or equals it plus a language suffix such as ".en" or ".trans".
package lyrics

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Line is one lyric line and its translation, keyed by the timestamp they
// share. A file with no translation leaves Translation empty.
type Line struct {
	TimeMs      int64  `json:"timeMs"`
	Text        string `json:"text"`
	Translation string `json:"translation"`
}

// Document is what the player draws. Synced is false for a plain-text lyric
// file, which the player shows without following the position.
type Document struct {
	Lines  []Line `json:"lines"`
	Synced bool   `json:"synced"`
	Source string `json:"source"`
}

// Load finds the lyric file for mediaPath and parses it. A missing file is
// reported as ((nil), nil): no lyrics is a normal state, not an engine error.
func Load(mediaPath string) (*Document, error) {
	main, translation := find(mediaPath)
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

var timestamp = regexp.MustCompile(`\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)

// parse reads the LRC timestamps into one line per timestamp. A line without a
// timestamp is kept as unsynced content; the caller can still show it.
func parse(path string) ([]Line, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	var (
		out    []Line
		synced bool
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
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
			out = append(out, Line{TimeMs: -1, Text: raw})
			continue
		}
		text := strings.TrimSpace(timestamp.ReplaceAllString(raw, ""))
		if text == "" {
			continue
		}
		for _, loc := range locs {
			ms := parseTime(raw[loc[2]:loc[3]], raw[loc[4]:loc[5]], raw[loc[6]:loc[7]])
			out = append(out, Line{TimeMs: ms, Text: text})
			synced = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, false, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TimeMs < out[j].TimeMs })

	return out, synced, nil
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

// find returns the main and translation lyric paths, either may be empty.
func find(mediaPath string) (string, string) {
	dir := filepath.Dir(mediaPath)
	base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	key := normalize(base)

	dirs := []string{dir, filepath.Join(dir, "lyrics")}
	var (
		main        string
		translation string
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
			match := matchScore(base, key, stem)
			if match == 0 {
				continue
			}
			path := filepath.Join(d, e.Name())
			if lang != "" {
				if translation == "" || match > 1 {
					translation = path
				}
				continue
			}
			if main == "" || match > 1 {
				main = path
			}
		}
	}

	return main, translation
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
