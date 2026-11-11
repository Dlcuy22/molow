package lyrics

import (
	"sort"
	"unicode/utf8"
)

// InstrumentalMark is the line that fills a long silence between two sung
// lines. The lyric pane draws it as notes instead of the character, and the
// mark only ever exists in memory; the sheet on disk stays as the provider
// wrote it.
const InstrumentalMark = "♪"

// instrumentalGapMs is the silence that earns a mark. A shorter pause reads as
// a breath between lines and stays empty.
const instrumentalGapMs = 4000

// instrumentalMinSamples is how many short gaps a sheet needs before its own
// pacing can estimate a line end.
const instrumentalMinSamples = 3

// MarkInstrumental returns a copy of lines with a mark in every silence longer
// than instrumentalGapMs, plus one at the end when durationMs runs past the
// last sung word. Word-timed lines end at their last word. A line-timed sheet
// has no end in its data, so the end is estimated from the sheet's own pacing:
// the characters it sings per millisecond, measured on the lines that run
// straight into the next one. Marks already in the input are dropped first, so
// a recomputation cannot stack them. durationMs zero leaves the tail alone.
func MarkInstrumental(lines []Line, durationMs int64) []Line {
	base := dropInstrumentalMarks(lines)
	rate := charsPerMs(base)

	out := make([]Line, 0, len(base)+4)
	inserted := false
	for i, line := range base {
		out = append(out, line)
		if i+1 >= len(base) {
			continue
		}
		next := base[i+1]
		if next.TimeMs <= 0 {
			continue
		}
		end := lineEnd(line, rate)
		if end <= 0 || next.TimeMs-end <= instrumentalGapMs {
			continue
		}
		out = append(out, Line{TimeMs: end, Text: InstrumentalMark})
		inserted = true
	}

	if durationMs > 0 && len(out) > 0 {
		last := out[len(out)-1]
		if end := lineEnd(last, rate); end > 0 && durationMs-end > instrumentalGapMs {
			out = append(out, Line{TimeMs: end, Text: InstrumentalMark})
			inserted = true
		}
	}

	if inserted || len(base) != len(lines) {
		return out
	}

	return lines
}

// dropInstrumentalMarks removes the marks a previous computation left, so the
// current threshold and duration decide the result alone.
func dropInstrumentalMarks(lines []Line) []Line {
	kept := make([]Line, 0, len(lines))
	for _, line := range lines {
		if line.Text != InstrumentalMark {
			kept = append(kept, line)
		}
	}

	return kept
}

// lineEnd is when a line stops being sung. A word-timed line knows; a
// line-timed one is estimated from its length at the sheet's own rate.
func lineEnd(line Line, rate float64) int64 {
	if line.TimeMs < 0 || line.Text == "" {
		return 0
	}
	if n := len(line.Words); n > 0 {
		if end := line.Words[n-1].EndMs; end > line.TimeMs {
			return end
		}

		return 0
	}
	chars := utf8.RuneCountInString(line.Text)
	if rate <= 0 || chars == 0 {
		return 0
	}

	return line.TimeMs + int64(float64(chars)/rate)
}

// charsPerMs measures how fast a sheet sings, from the lines that follow the
// previous one within the gap threshold. Those are contiguous lines, so their
// spacing is a line duration, not a silence. Fewer than three samples is no
// measurement, and the sheet keeps no line-timed marks.
func charsPerMs(lines []Line) float64 {
	var samples []float64
	for i := 0; i+1 < len(lines); i++ {
		a, b := lines[i], lines[i+1]
		if a.TimeMs < 0 || b.TimeMs <= a.TimeMs {
			continue
		}
		delta := b.TimeMs - a.TimeMs
		if delta > int64(instrumentalGapMs) {
			continue
		}
		chars := utf8.RuneCountInString(a.Text)
		if chars == 0 {
			continue
		}
		samples = append(samples, float64(chars)/float64(delta))
	}
	if len(samples) < instrumentalMinSamples {
		return 0
	}
	sort.Float64s(samples)

	return samples[len(samples)/2]
}
