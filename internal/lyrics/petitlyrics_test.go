package lyrics

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// The WSY fixture is a real response for "いますぐ輪廻" by なきそ (lyricsId
// 3878442) captured from the live HF Player endpoint. petitWrap shapes one
// inner WSY into the envelope the endpoint answers with.

func petitFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/wsy_imasugu.xml")
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func petitWrap(status, lyricsType string, wsy []byte) string {
	body := base64.StdEncoding.EncodeToString(wsy)
	return `<response><status>` + status + `</status><songs><matchedCount>1</matchedCount><returnedCount>1</returnedCount>` +
		`<song><lyricsId>3878442</lyricsId><title>いますぐ輪廻</title><artist>なきそ</artist><duration>122482</duration>` +
		`<lyricsType>` + lyricsType + `</lyricsType><lyricsData>` + body + `</lyricsData></song></songs></response>`
}

func petitTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestParseWSYShapes(t *testing.T) {
	lines, err := parseWSY(petitFixture(t))
	if err != nil {
		t.Fatal(err)
	}

	// linenum in the fixture is 51, of which 9 are blank separators; the
	// reader must see the 42 lyric lines.
	if len(lines) != 42 {
		t.Fatalf("lines = %d, want 42", len(lines))
	}
	first := lines[0]
	if first.Text != "いますぐ輪廻 今回も結ばれないね" {
		t.Fatalf("first line = %q", first.Text)
	}
	if len(first.Words) != 16 {
		t.Fatalf("first line words = %d, want 16", len(first.Words))
	}
	if w := first.Words[0]; w.Text != "い" || w.StartMs != 775 || w.EndMs != 838 {
		t.Fatalf("first word = %+v", w)
	}

	// A karaoke sweep lives or dies with the timing: every word ends at or
	// after its start, and words run forward through the line.
	for i, l := range lines {
		prevEnd := l.start
		for j, w := range l.Words {
			if w.StartMs < prevEnd-5 || w.EndMs < w.StartMs {
				t.Fatalf("line %d word %d timing off: %+v (prev end %d)", i, j, w, prevEnd)
			}
			prevEnd = w.EndMs
		}
	}
}

func TestParseWSYDropsSeparatorLines(t *testing.T) {
	// Separators already gone is the shape test's 42; the negative pin is
	// the plain-only sheet, where a text line without words must stay.
	lines, err := parseWSY([]byte(
		`<wsy><linenum>2</linenum>` +
			`<line><linestring>a</linestring><wordnum>0</wordnum></line>` +
			`<line><linestring>b</linestring><wordnum>0</wordnum></line></wsy>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Text != "a" || len(lines[0].Words) != 0 {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestWSYToWordBlockLRC(t *testing.T) {
	lines, err := parseWSY(petitFixture(t))
	if err != nil {
		t.Fatal(err)
	}

	raw := petitToLRC(lines)
	rows := strings.Split(raw, "\n")

	// The canonical shape the loader already reads: a [mm:ss.cc] timestamped
	// line, then its word block with second timestamps. The fixture's first
	// line carries all 16 words of the phrase.
	if rows[0] != "[00:00.77]いますぐ輪廻 今回も結ばれないね" {
		t.Fatalf("timestamp line = %q", rows[0])
	}
	if !strings.HasPrefix(rows[1], "<い:0.775:0.838|ま:0.838:0.888|す:0.888:1.054|") {
		t.Fatalf("word block opening = %q", rows[1])
	}
	if !strings.HasSuffix(rows[1], "ね:3.101:3.267>") {
		t.Fatal("word block does not close on the phrase's final word")
	}
}

func TestWSYToLRCSkipsUntimedLines(t *testing.T) {
	lines, err := parseWSY([]byte(
		`<wsy><linenum>3</linenum>` +
			`<line><starttime></starttime><wordnum>0</wordnum></line>` +
			`<line><linestring>timed</linestring><wordnum>1</wordnum><word><starttime>1000</starttime><endtime>1500</endtime><wordstring>timed</wordstring></word></line>` +
			`</wsy>`))
	if err != nil {
		t.Fatal(err)
	}

	raw := petitToLRC(lines)
	if strings.Contains(raw, "[]") {
		t.Fatalf("an untimed line rendered a blank stamp: %q", raw)
	}
	if !strings.Contains(raw, "[00:01.00]timed") {
		t.Fatalf("timed line missing: %q", raw)
	}
}

// TestPetitFetchFallsBackToTheTitle pins the artist miss the live catalogue
// showed: the endpoint stores the JP artist form, while tags often carry the
// Latin one ("NAKISO" matches nothing, "なきそ" does). The provider walks the
// alias pairs, then a title-only attempt accepted only on a duration match.
func TestPetitFetchFallsBackToTheTitle(t *testing.T) {
	wrapped := petitWrap("00000000", "3", petitFixture(t))
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		bodies = append(bodies, r.PostForm.Get("key_artist"))
		_, _ = w.Write([]byte(wrapped))
	}))
	t.Cleanup(srv.Close)
	restore := swapEndpoints(map[string]string{"petitlyrics": srv.URL})
	defer restore()

	raw, err := fetchPetitLyrics(context.Background(), testClient(t),
		TrackMeta{Title: "いますぐ輪廻", Artist: "NAKISO", DurationSec: 122})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "[00:00.77]いますぐ輪廻") {
		t.Fatalf("raw = %q", strings.Split(raw, "\n")[0])
	}
	if len(bodies) < 2 {
		t.Fatalf("artist attempts = %v, want an alias pass then the title-only one", bodies)
	}
	if last := bodies[len(bodies)-1]; last != "" {
		t.Fatalf("the last attempt must be title-only, artist = %q", last)
	}
}

// TestPetitFetchRejectsADurationAlien pins the guard's teeth: a track of a
// different length with the same title cannot be taken home by the title-only
// attempt.
func TestPetitFetchRejectsADurationAlien(t *testing.T) {
	wrong := strings.Replace(petitWrap("00000000", "3", petitFixture(t)), "00000000", "00000000", 1)
	srv := petitTestServer(t, wrong)
	restore := swapEndpoints(map[string]string{"petitlyrics": srv.URL})
	defer restore()

	if _, err := fetchPetitLyrics(context.Background(), testClient(t),
		TrackMeta{Title: "いますぐ輪廻", Artist: "NAKISO", DurationSec: 300}); err == nil {
		t.Fatal("a 300 s tag must not take a 122 s song")
	}
}

// TestPetitFetchQueriesTheBilingualParts pins the deterministic catalogue
// bridge: a YTM track titled "JP - EN" only reaches petit's JP-stored sheet
// when the alias parts are queried. The compound query answers with a
// different song of the same artist (the English rendition), which the tier
// guard skips; the bare JP query answers with our song.
func TestPetitFetchQueriesTheBilingualParts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("key_title") != "いますぐ輪廻" {
			// The English rendition: right artist shape, line-synced, wrong
			// sheet, the shape the compound query brings back.
			_, _ = w.Write([]byte(`<response><status>00000000</status><songs>` +
				`<song><lyricsId>4177593</lyricsId><title>Retry Now</title><artist>なきそ</artist><duration>188037</duration>` +
				`<lyricsType>2</lyricsType><lyricsData></lyricsData></song></songs></response>`))

			return
		}
		_, _ = w.Write([]byte(petitWrap("00000000", "3", petitFixture(t))))
	}))
	t.Cleanup(srv.Close)
	restore := swapEndpoints(map[string]string{"petitlyrics": srv.URL})
	defer restore()

	raw, err := fetchPetitLyrics(context.Background(), testClient(t),
		TrackMeta{Title: "いますぐ輪廻 - Retry Now", Artist: "なきそ", DurationSec: 122})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "[00:00.77]いますぐ輪廻") {
		t.Fatalf("raw = %q", strings.Split(raw, "\n")[0])
	}
}

func TestPetitFetchParsesTheWordSyncAnswer(t *testing.T) {
	srv := petitTestServer(t, petitWrap("00000000", "3", petitFixture(t)))
	restore := swapEndpoints(map[string]string{"petitlyrics": srv.URL})
	defer restore()

	raw, err := fetchPetitLyrics(context.Background(), testClient(t),
		TrackMeta{Title: "いますぐ輪廻", Artist: "なきそ", Album: "いますぐ輪廻", DurationSec: 122})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "[00:00.77]いますぐ輪廻") {
		t.Fatalf("raw = %q", strings.Split(raw, "\n")[0])
	}
	if q := Quality(raw); q != 3 {
		t.Fatalf("quality = %d, want 3", q)
	}
}

func TestPetitFetchMissesOnALesserTier(t *testing.T) {
	srv := petitTestServer(t, petitWrap("00000000", "1", []byte("plain text")))
	restore := swapEndpoints(map[string]string{"petitlyrics": srv.URL})
	defer restore()

	if _, err := fetchPetitLyrics(context.Background(), testClient(t),
		TrackMeta{Title: "いますぐ輪廻", Artist: "なきそ", DurationSec: 122}); err == nil {
		t.Fatal("a lyricsType 1 answer must miss")
	}
}

func TestPetitFetchMissesOnTheStatus(t *testing.T) {
	srv := petitTestServer(t, `<response><status>00000001</status><songs></songs></response>`)
	restore := swapEndpoints(map[string]string{"petitlyrics": srv.URL})
	defer restore()

	if _, err := fetchPetitLyrics(context.Background(), testClient(t), TrackMeta{Title: "X", Artist: "Y"}); err == nil {
		t.Fatal("a nonzero status must miss")
	}
}

// TestPetitFetchCarriesTheSDKForm pins the request bodies the endpoint
// expects, so a refactor cannot silently change what the authoritative
// server sees. The alias attempt carries the artist, the title-only attempt
// that follows gives the artist key blank.
func TestPetitFetchCarriesTheSDKForm(t *testing.T) {
	var bodies []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		bodies = append(bodies, r.PostForm)
	}))
	t.Cleanup(srv.Close)
	restore := swapEndpoints(map[string]string{"petitlyrics": srv.URL + "/api"})
	defer restore()

	_, _ = fetchPetitLyrics(context.Background(), testClient(t),
		TrackMeta{Title: "いますぐ輪廻", Artist: "なきそ", Album: "いますぐ輪廻"})

	if len(bodies) == 0 {
		t.Fatal("no attempt was sent")
	}
	sdk := bodies[0].Encode()
	for _, want := range []string{
		"clientAppId=on354007", "appName=HF+Player", "terminalType=0", "lyricsType=3",
		"key_title=%E3%81%84%E3%81%BE%E3%81%99%E3%81%90%E8%BC%AA%E5%BB%BB",
		"key_artist=%E3%81%AA%E3%81%8D%E3%81%9D",
		"key_album=%E3%81%84%E3%81%BE%E3%81%99%E3%81%90%E8%BC%AA%E5%BB%BB",
	} {
		if !strings.Contains(sdk, want) {
			t.Fatalf("form = %q, missing %q", sdk, want)
		}
	}
	if bodies[len(bodies)-1].Get("key_artist") != "" {
		t.Fatalf("the final attempt must be title-only, artist = %q", bodies[len(bodies)-1].Get("key_artist"))
	}
}

// TestChainWordSyncedFromPetitShortCircuits pins the reason PetitLyrics
// leads the chain: its word-synced answer ends the run, and the line-synced
// sheet the second provider had ready becomes irrelevant.
func TestChainWordSyncedFromPetitShortCircuits(t *testing.T) {
	called := map[string]int{}
	others := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called["others"]++
		_, _ = w.Write([]byte(""))
	}))
	t.Cleanup(others.Close)
	petit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called["petit"]++
		_, _ = w.Write([]byte(petitWrap("00000000", "3", petitFixture(t))))
	}))
	t.Cleanup(petit.Close)
	restore := swapEndpoints(map[string]string{
		"petitlyrics":  petit.URL,
		"betterlyrics": others.URL + "/getLyrics",
		"lyricsplus":   others.URL,
		"kugou":        others.URL,
		"lrclib":       others.URL,
	})
	defer restore()

	payload, source, err := Fetch(context.Background(), testClient(t),
		TrackMeta{Title: "いますぐ輪廻", Artist: "なきそ", DurationSec: 122}, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if source != "petitlyrics" || Quality(payload) != 3 {
		t.Fatalf("source %q quality = %d", source, Quality(payload))
	}
	if called["others"] != 0 {
		t.Fatalf("word-synced answer must end the run, others called %d times", called["others"])
	}
	if called["petit"] != 1 {
		t.Fatalf("petit called %d times", called["petit"])
	}
}
