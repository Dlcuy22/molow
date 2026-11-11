// PetitLyrics provider: the Onkyo HF Player SDK endpoint, the route to
// Japanese word-synced sheets the other providers do not carry. Only the word
// tier answers; a lesser tier is a miss.

package lyrics

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// petitForm builds the fixed SDK identity the endpoint has accepted since the
// HF Player days and asks for tier 3, word sync, outright.
func petitForm(meta TrackMeta) url.Values {
	form := url.Values{
		"clientAppId":  {"on354007"},
		"appName":      {"HF Player"},
		"sdkVer":       {"1.3.4"},
		"userId":       {"642bbdc6-128a-4b67-a10b-bc09191b0cfe"},
		"terminalType": {"0"},
		"lyricsType":   {"3"},
		"index":        {"0"},
		"maxcount":     {"1"},
		"logFlag":      {"0"},
		"verCode":      {"212"},
		"verName":      {"2.7.0"},
	}
	form.Set("key_title", meta.Title)
	form.Set("key_artist", meta.Artist)
	if meta.Album != "" {
		form.Set("key_album", meta.Album)
	}

	return form
}

// petitEnvelope is the outer XML around one song. The record carries the
// title, artist and duration the title-only attempt needs to prove itself.
type petitEnvelope struct {
	Status string      `xml:"status"`
	Songs  []petitSong `xml:"songs>song"`
}

type petitSong struct {
	LyricsID   string `xml:"lyricsId"`
	Title      string `xml:"title"`
	Artist     string `xml:"artist"`
	Duration   string `xml:"duration"`
	LyricsType string `xml:"lyricsType"`
	LyricsData string `xml:"lyricsData"`
}

// petitSheet mirrors the WSY XML tree.
type petitSheet struct {
	Lines []petitWSYLine `xml:"line"`
}

type petitWSYLine struct {
	Linestring string         `xml:"linestring"`
	Starttime  string         `xml:"starttime"`
	Words      []petitWSYWord `xml:"word"`
}

type petitWSYWord struct {
	Starttime  string `xml:"starttime"`
	Endtime    string `xml:"endtime"`
	Wordstring string `xml:"wordstring"`
}

// petitLine is one parsed lyric line, its words in song order.
type petitLine struct {
	Text  string
	Words []struct {
		Text    string
		StartMs int
		EndMs   int
	}
	start int
}

// petitInt reads a decimal millisecond stamp, and anything unreadable becomes
// zero.
func petitInt(s string) int {
	var n int
	_, _ = fmt.Sscanf(strings.TrimSpace(s)+" ", "%d", &n)

	return n
}

// parseWSY reads one WSY sheet. A blank separator, with no text and no word,
// is dropped, so the line count the player shows matches the sheet; a plain
// line without words stays.
func parseWSY(data []byte) ([]petitLine, error) {
	var sheet petitSheet
	if err := xml.Unmarshal(data, &sheet); err != nil {
		return nil, fmt.Errorf("petitlyrics: bad wsy: %w", err)
	}

	out := make([]petitLine, 0, len(sheet.Lines))
	for _, l := range sheet.Lines {
		line := petitLine{Text: strings.TrimSpace(l.Linestring), start: petitInt(l.Starttime)}
		for _, w := range l.Words {
			text := strings.TrimSpace(w.Wordstring)
			line.Words = append(line.Words, struct {
				Text    string
				StartMs int
				EndMs   int
			}{
				Text:    text,
				StartMs: petitInt(w.Starttime),
				EndMs:   petitInt(w.Endtime),
			})
		}
		// A line with no element-level stamp starts at its first word.
		if line.start == 0 && len(line.Words) > 0 {
			line.start = line.Words[0].StartMs
		}
		content := line.Text != ""
		for _, w := range line.Words {
			if w.Text != "" {
				content = true
				break
			}
		}
		if content {
			out = append(out, line)
		}
	}

	return out, nil
}

// petitStamp renders a centisecond LRC timestamp, "00:00.77" for 775 ms.
func petitStamp(ms int) string {
	if ms < 0 {
		ms = 0
	}
	total := ms / 10 // centiseconds

	return fmt.Sprintf("%02d:%02d.%02d", total/6000, (total/100)%60, total%100)
}

// petitSec renders the word-block second stamps, trimmed: "0.775" for 775 ms.
func petitSec(ms int) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', -1, 64)
}

// petitToLRC returns the canonical word-block LRC: a timestamped line, then
// its word block. Lines with no timing are skipped, because the loader follows
// a sheet by its stamps.
func petitToLRC(lines []petitLine) string {
	var b strings.Builder
	for _, l := range lines {
		if len(l.Words) == 0 && l.start == 0 {
			continue
		}

		b.WriteByte('[')
		b.WriteString(petitStamp(l.start))
		b.WriteString("]")
		b.WriteString(l.Text)
		b.WriteString("\n")
		if len(l.Words) > 0 {
			b.WriteString("<")
			for i, w := range l.Words {
				if i > 0 {
					b.WriteString("|")
				}
				b.WriteString(petitEscape(w.Text))
				b.WriteString(":")
				b.WriteString(petitSec(w.StartMs))
				b.WriteString(":")
				b.WriteString(petitSec(w.EndMs))
			}
			b.WriteString(">")
			b.WriteString("\n")
		}
	}

	return b.String()
}

// petitEscape keeps a literal pipe or angle bracket from splitting the word
// block into pieces.
func petitEscape(s string) string {
	if !strings.ContainsAny(s, "|<>") {
		return s
	}

	return strings.NewReplacer("|", "／", "<", "《", ">", "》").Replace(s)
}

// petitParseResponse picks the first song that passes the guard and carries a
// word-tier sheet. The title must match one of the searched titles, and if the
// attempt included an artist, the artist must match too. A title-only attempt
// has no artist to prove it, so the duration is checked instead.
func petitParseResponse(body []byte, titles []string, artist string, durationSec int) (string, error) {
	var env petitEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("petitlyrics: bad envelope: %w", err)
	}
	if env.Status != "00000000" {
		return "", fmt.Errorf("petitlyrics: status %s", env.Status)
	}
	for _, s := range env.Songs {
		if s.LyricsType != "3" || s.LyricsData == "" {
			continue
		}
		if !contained(s.Title, titles) {
			continue
		}
		if artist != "" {
			if !contained(s.Artist, []string{artist}) {
				continue
			}
		} else if durationSec > 0 {
			// No artist proof: the duration is the identification, and a
			// different recording must not pass for the track.
			d := petitInt(s.Duration)/1000 - durationSec
			if d < -petitToleranceSec || d > petitToleranceSec {
				continue
			}
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s.LyricsData))
		if err != nil {
			return "", fmt.Errorf("petitlyrics: bad lyrics base64: %w", err)
		}
		lines, err := parseWSY(raw)
		if err != nil {
			return "", err
		}
		if text := petitToLRC(lines); text != "" {
			return text, nil
		}
	}

	return "", fmt.Errorf("no lyrics")
}

// petitToleranceSec is how far a title-only match may drift from the track's
// length before it could be a different recording.
const petitToleranceSec = 5

// petitAttempt is one key pair the endpoint gets a POST from.
type petitAttempt struct {
	title, artist string
}

// fetchPetitLyrics sends the search requests. The endpoint matches the artist
// exactly and stores the Japanese form, so the alias pairs are tried first,
// then a title-only attempt that the duration has to vouch for.
func fetchPetitLyrics(ctx context.Context, h *http.Client, meta TrackMeta) (string, error) {
	titles := titleAliases(meta)

	var attempts []petitAttempt
	for _, p := range pairs(meta, 2) {
		attempts = append(attempts, petitAttempt{title: p[1], artist: p[0]})
	}
	if len(attempts) > 8 {
		attempts = attempts[:8]
	}
	for _, t := range titles {
		attempts = append(attempts, petitAttempt{title: t})
	}

	for _, p := range attempts {
		form := petitForm(meta)
		form.Set("key_title", p.title)
		form.Set("key_artist", p.artist)
		if meta.Album != "" {
			form.Set("key_album", meta.Album)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, providersEnd.petitlyrics, strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := h.Do(req)
		if err != nil {
			return "", err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		if raw, err := petitParseResponse(body, titles, p.artist, meta.DurationSec); err == nil {
			return raw, nil
		}
	}

	return "", fmt.Errorf("no lyrics")
}
