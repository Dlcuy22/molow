package lyrics

import (
	"encoding/xml"
	"strconv"
	"strings"
)

// ParseTTML turns an Apple-Music-style TTML document into synced lines. Every
// span with a begin and an end is one timed word, and a plain timed paragraph
// is a line-synced line. Background vocals, translations and romanizations are
// skipped. A nil result means there was no usable content, which is normal for
// these endpoints.
func ParseTTML(src string) *Document {
	var root struct {
		XMLName xml.Name `xml:"tt"`
		Body    struct {
			Divs []ttmlDiv `xml:"div"`
		} `xml:"body"`
	}
	if err := xml.Unmarshal([]byte(src), &root); err != nil {
		return nil
	}

	offset := ttmlOffset(src)
	var lines []Line
	for _, div := range root.Body.Divs {
		lines = ttmlDivLines(div, offset, lines)
	}
	if len(lines) == 0 {
		return nil
	}
	setWordEnds(lines)

	return &Document{Lines: lines, Synced: true, Source: "ttml"}
}

type ttmlDiv struct {
	Agent string     `xml:"agent,attr"`
	Divs  []ttmlDiv  `xml:"div"`
	Paras []ttmlPara `xml:"p"`
}

// ttmlDivLines flattens the div tree in document order; the agent each p
// carries an owner for is dropped with the rest of the voice metadata.
func ttmlDivLines(div ttmlDiv, offset float64, out []Line) []Line {
	for _, p := range div.Paras {
		if line, ok := ttmlLine(p, offset); ok {
			out = append(out, line)
		}
	}
	for _, nested := range div.Divs {
		out = ttmlDivLines(nested, offset, out)
	}

	return out
}

type ttmlPara struct {
	Agent string     `xml:"agent,attr"`
	Begin string     `xml:"begin,attr"`
	End   string     `xml:"end,attr"`
	Text  string     // the paragraph's spoken text, spacing preserved
	Spans []ttmlSpan `xml:"span"`
}

// UnmarshalXML keeps the paragraph's mixed content in order. A provider writes
// word spans separated by text nodes ("We're</span> <span>both") and splits a
// word into adjacent spans with nothing between them ("mir</span><span>a").
// Reading span text alone loses every space, so the real text is the spans and
// the text nodes together.
func (p *ttmlPara) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	p.Agent = ttmlAttr(start, "agent")
	p.Begin = ttmlAttr(start, "begin")
	p.End = ttmlAttr(start, "end")

	var text strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch node := tok.(type) {
		case xml.StartElement:
			if node.Name.Local != "span" {
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			var span ttmlSpan
			if err := d.DecodeElement(&span, &node); err != nil {
				return err
			}
			p.Spans = append(p.Spans, span)
			if ttmlSpoken(span.Role) {
				text.WriteString(span.Text)
			}
		case xml.CharData:
			text.Write(node)
		case xml.EndElement:
			if node.Name == start.Name {
				p.Text = text.String()
				return nil
			}
		}
	}
}

// ttmlSpoken reports whether a span belongs on the lyric line. Background
// vocals, translations and romanizations are timed but never drawn.
func ttmlSpoken(role string) bool {
	switch role {
	case "x-bg", "x-translation", "x-roman":
		return false
	}

	return true
}

// ttmlAttr reads one attribute by local name, which is how the decoder matches
// them across the namespaces these documents use.
func ttmlAttr(start xml.StartElement, name string) string {
	for _, attr := range start.Attr {
		if attr.Name.Local == name {
			return attr.Value
		}
	}

	return ""
}

type ttmlSpan struct {
	Role  string `xml:"role,attr"`
	Begin string `xml:"begin,attr"`
	End   string `xml:"end,attr"`
	Text  string `xml:",chardata"`
}

// ttmlOffset reads the head's `lyricOffset`, which is an attribute of a
// namespaced element the decoder's tag matching does not reliably reach. It
// shifts every timestamp the document carries.
func ttmlOffset(src string) float64 {
	const key = `lyricOffset="`
	i := strings.Index(src, key)
	if i < 0 {
		return 0
	}
	rest := src[i+len(key):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return 0
	}

	return ttmlTime(rest[:end])
}

// ttmlLine builds one line from a paragraph: the direct timed spans are the
// words, and the text they spell is the line text.
func ttmlLine(p ttmlPara, offset float64) (Line, bool) {
	line := Line{TimeMs: toMs(ttmlTime(p.Begin) + offset)}

	for _, span := range p.Spans {
		if !ttmlSpoken(span.Role) {
			continue
		}
		if span.Begin == "" || span.End == "" {
			continue
		}
		text := strings.TrimSpace(span.Text)
		if text == "" {
			continue
		}
		line.Words = append(line.Words, Word{
			Text:    text,
			StartMs: toMs(ttmlTime(span.Begin) + offset),
			EndMs:   toMs(ttmlTime(span.End) + offset),
		})
	}

	// The paragraph's own mixed text owns the spacing; the word list only
	// holds the sung segments and cannot show where the gaps belong.
	line.Text = strings.TrimSpace(p.Text)
	if line.Text == "" && len(line.Words) > 0 {
		line.Text = joinWordTexts(line.Words)
	}
	if line.Text == "" || (p.Begin == "" && len(line.Words) == 0) {
		return Line{}, false
	}

	return line, true
}

// ttmlTime reads the forms these documents mix: a decimal "12.34", a "12.34s",
// a millisecond "1234ms", and the clock "01:02:03.456". Anything unreadable
// is zero, which shifts nothing.
func ttmlTime(s string) float64 {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "ms") {
		if v, err := strconv.ParseFloat(strings.TrimSuffix(s, "ms"), 64); err == nil {
			return v / 1000
		}

		return 0
	}
	if strings.HasSuffix(s, "s") {
		s = strings.TrimSuffix(s, "s")
	} else if strings.HasSuffix(s, "h") {
		if v, err := strconv.ParseFloat(strings.TrimSuffix(s, "h"), 64); err == nil {
			return v * 3600
		}

		return 0
	}
	if strings.Contains(s, ":") {
		sec := 0.0
		for _, part := range strings.Split(s, ":") {
			v, err := strconv.ParseFloat(part, 64)
			if err != nil {
				return 0
			}
			sec = sec*60 + v
		}

		return sec
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}

	return 0
}

func toMs(sec float64) int64 {
	if sec <= 0 {
		return 0
	}

	return int64(sec*1000 + 0.5)
}
