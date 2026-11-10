package backend

import (
	"os"
	"testing"

	ytm "github.com/dlcuy22/ytm-go"
)

// TestLyricsYTMLive exercises the real lyric lookup end to end: a catalogue
// reference, the metadata call that carries the lyrics tab, and the sheet
// itself. It is opt-in because it needs the network:
//
//	MOLOW_LIVE=1 go test ./backend -run TestLyricsYTMLive -v
//
// No account is needed; the lyric endpoints answer anonymously.
func TestLyricsYTMLive(t *testing.T) {
	if os.Getenv("MOLOW_LIVE") == "" {
		t.Skip("set MOLOW_LIVE=1 to run the live lyric check")
	}

	client := ytm.NewClient()
	s := lyricsService(&ytmProvider{
		index:    newYTMIndex(),
		sheets:   make(map[string]*ytm.Lyrics),
		loadSong: client.LoadSong,
		lyrics:   client.GetSongLyricsTimed,
	})

	doc, err := s.Lyrics("ytm:Kr4EQDVETuA") // Michael Jackson - Billie Jean
	if err != nil {
		t.Fatalf("Lyrics: %v", err)
	}
	if doc == nil || len(doc.Lines) == 0 {
		t.Fatal("no lyric lines for a track known to have them")
	}
	if !doc.Synced {
		t.Error("the catalogue sheet should be timed")
	}
	t.Logf("source=%q synced=%v lines=%d first=%q", doc.Source, doc.Synced, len(doc.Lines), doc.Lines[0].Text)
}
