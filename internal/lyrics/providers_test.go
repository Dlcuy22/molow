package lyrics

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// The provider fixtures run each endpoint family against httptest servers;
// swapEndpoints points the chain at them and restores the defaults after.

func testClient(t *testing.T) *http.Client {
	t.Helper()

	return &http.Client{Timeout: 2 * time.Second}
}

// swapEndpoints points the chain at test URLs and returns the restore. Each
// endpoint keeps the exact shape its call site builds, trailing "?" included
// where the production default carries one.
func swapEndpoints(m map[string]string) func() {
	saved := providersEnd
	if base, ok := m["betterlyrics"]; ok {
		providersEnd.betterLyrics = base + "/getLyrics"
	}
	if base, ok := m["lyricsplus"]; ok {
		providersEnd.lyricsPlus = []string{base}
	}
	if base, ok := m["binimum"]; ok {
		providersEnd.binimum = base + "?"
	}
	if base, ok := m["kugou"]; ok {
		providersEnd.kugouSong = base + "?"
		providersEnd.kugouLyrics = base + "?"
		providersEnd.kugouFetch = base + "?"
	}
	if base, ok := m["lrclib"]; ok {
		providersEnd.lrcGet = base + "?"
		providersEnd.lrcSearch = base + "?"
	}
	if base, ok := m["petitlyrics"]; ok {
		providersEnd.petitlyrics = base + "/GetPetitLyricsData.php"
	}

	return func() { providersEnd = saved }
}

const wordTTML = `<tt><body><div><p begin="00:01" end="00:04"><span begin="00:01" end="00:02">hi</span><span begin="00:02" end="00:04">there</span></p></div></body></tt>`

func TestTransformLyricsPlusWordPayload(t *testing.T) {
	resp := &lyricsPlusResult{
		Type: "Word",
		Lyrics: []lyricsPlusSyntax{
			{
				Time: 4230,
				Text: "just a line",
				Syllabus: []lyricsPlusSylla{
					{Time: 4230, Duration: 200, Text: "just"},
					{Time: 4430, Duration: 200, Text: "hi"},
					{Time: 4630, Duration: 500, Text: "line"},
				},
			},
			// A line whose text is the authority: the rebuilt text must not
			// grow spaces the payload never had.
			{
				Time: 7200,
				Text: "あのね",
				Syllabus: []lyricsPlusSylla{
					{Time: 7200, Duration: 300, Text: "あの"},
					{Time: 7500, Duration: 300, Text: "ね"},
				},
			},
		},
	}
	raw, err := lyricsPlusToLRC(resp)
	if err != nil {
		t.Fatal(err)
	}
	lines, synced, parseErr := Parse(raw)
	if parseErr != nil || !synced || len(lines) != 2 {
		t.Fatalf("round trip: %q -> %+v err %v", raw, lines, parseErr)
	}
	if len(lines[0].Words) != 3 {
		t.Fatalf("words: %+v", lines[0].Words)
	}
	w := lines[0].Words[2]
	if w.Text != "line" || w.StartMs != 4630 || w.EndMs != 5130 {
		t.Fatalf("last word: %+v", w)
	}
	if lines[1].Text != "あのね" {
		t.Fatalf("line text = %q, want the payload's own あのね", lines[1].Text)
	}
}

func TestLyricsPlusStickyServers(t *testing.T) {
	restore := swapEndpoints(nil)
	defer restore()
	lyricsPlusSticky = providersEnd.lyricsPlus[1]
	defer func() { lyricsPlusSticky = "" }()

	got := lyricsPlusStickyServers()
	if got[0] != providersEnd.lyricsPlus[1] || len(got) != len(providersEnd.lyricsPlus) {
		t.Fatalf("sticky order: %v", got)
	}
}

func TestKuGouStripsCredits(t *testing.T) {
	head := "[00:00.10]Composer: credit\n[00:00.20]Lyrics by: credit\n[00:00.30]Arranged: credit\n"
	body := "[00:01.00]そら\n[00:02.00]かぜ"
	tail := "[02:00.00]Translated by: credit\n"

	if got := kugouNormalize(head + body + "\n" + tail); got != body {
		t.Fatalf("normalized: %q", got)
	}
}

func TestFetchChainStopsAtAWordHit(t *testing.T) {
	// BetterLyrics answers with a word-tier sheet; the line-tier provider
	// behind it must never be called.
	var lrclibCalled bool
	better := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ttml":` + strconv.Quote(wordTTML) + `}`))
	}))
	lrclib := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lrclibCalled = true
		_, _ = w.Write([]byte(`[{"trackName":"Song","artistName":"Artist","duration":100,"syncedLyrics":"[00:01.00]line"}]`))
	}))
	t.Cleanup(better.Close)
	t.Cleanup(lrclib.Close)
	restore := swapEndpoints(map[string]string{"betterlyrics": better.URL, "lrclib": lrclib.URL})
	defer restore()

	payload, source, err := Fetch(context.Background(), testClient(t),
		TrackMeta{Title: "Song", Artist: "Artist", DurationSec: 100}, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if source != "betterlyrics" || Quality(payload) != 3 {
		t.Fatalf("source %q quality(%.40s...) = %d", source, payload, Quality(payload))
	}
	if lrclibCalled {
		t.Fatal("chain kept asking after a word-synced hit")
	}
}

func TestFetchChainWordFromLaterProviderWins(t *testing.T) {
	// BetterLyrics misses; LyricsPlus' word-tier answer beats the
	// line-synced answer waiting behind it.
	better := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	lrclib := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a word hit from LyricsPlus must end the chain")
	}))
	lyricsplus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"type":"Word","lyrics":[{"time":1000,"text":"hey","syllabus":[{"time":1000,"duration":3000,"text":"hey"}]}]}`))
	}))
	t.Cleanup(better.Close)
	t.Cleanup(lrclib.Close)
	t.Cleanup(lyricsplus.Close)
	restore := swapEndpoints(map[string]string{
		"betterlyrics": better.URL,
		"lyricsplus":   lyricsplus.URL,
		"lrclib":       lrclib.URL,
	})
	defer restore()

	payload, source, err := Fetch(context.Background(), testClient(t),
		TrackMeta{Title: "Song", Artist: "Artist", DurationSec: 100}, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if source != "lyricsplus" || Quality(payload) != 3 {
		t.Fatalf("source %q quality = %d", source, Quality(payload))
	}
}

func TestFetchChainFallsThroughToLrcLib(t *testing.T) {
	// Nothing has words; LrcLib is last but still answers the chain.
	better := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	lyricsplus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	kugou := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"info":[]}}`))
	}))
	lrclib := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"trackName":"Song","artistName":"Artist","duration":100,"syncedLyrics":"[00:01.00]line"}]`))
	}))
	for _, s := range []*httptest.Server{better, lyricsplus, kugou, lrclib} {
		t.Cleanup(s.Close)
	}
	restore := swapEndpoints(map[string]string{
		"betterlyrics": better.URL,
		"lyricsplus":   lyricsplus.URL,
		"binimum":      kugou.URL,
		"kugou":        kugou.URL,
		"lrclib":       lrclib.URL,
	})
	defer restore()

	payload, source, err := Fetch(context.Background(), testClient(t),
		TrackMeta{Title: "Song", Artist: "Artist", DurationSec: 100}, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if source != "lrclib" || Quality(payload) != 2 {
		t.Fatalf("source %q quality = %d", source, Quality(payload))
	}
}

func TestFetchSkipsDisabledProviders(t *testing.T) {
	every := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a disabled provider was called: %s", r.URL.Path)
	}))
	t.Cleanup(every.Close)
	restore := swapEndpoints(map[string]string{
		"betterlyrics": every.URL,
		"lyricsplus":   every.URL,
		"kugou":        every.URL,
		"lrclib":       every.URL,
	})
	defer restore()

	if _, _, err := Fetch(context.Background(), testClient(t),
		TrackMeta{Title: "Song", Artist: "Artist"}, map[string]bool{
			"betterlyrics": false, "lyricsplus": false, "kugou": false, "lrclib": false,
		}, nil); err == nil {
		t.Fatal("the chain must miss with everything disabled")
	}
}

func TestLrcLibAliasContainment(t *testing.T) {
	// The query in one half of a dual-language tag must still match a
	// catalogue entry carrying the other half.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"trackName":"夕景","artistName":"なとり","duration":100,"syncedLyrics":"[00:01.00]line"},{"trackName":"Other","artistName":"Someone else","duration":100,"syncedLyrics":"[00:01.00]no"}]`))
	}))
	t.Cleanup(srv.Close)
	restore := swapEndpoints(map[string]string{"lrclib": srv.URL})
	defer restore()

	got, err := fetchLrcLib(context.Background(), testClient(t),
		TrackMeta{Title: "夕景", Artist: "natori / なとり", DurationSec: 100})
	if err != nil {
		t.Fatal(err)
	}
	if got != "[00:01.00]line" {
		t.Fatalf("got %q", got)
	}
}
