package backend

import (
	"context"
	"errors"
	"fmt"
	"testing"

	ytm "github.com/dlcuy22/ytm-go"
)

// lyricProvider builds a provider whose catalogue calls are fakes, so the lyric
// path is exercised without touching the network.
func lyricProvider(loadSong func(context.Context, string) (*ytm.Song, error), lyrics func(context.Context, string) (*ytm.Lyrics, error)) *ytmProvider {
	return &ytmProvider{
		index:    newYTMIndex(),
		sheets:   make(map[string]*ytm.Lyrics),
		loadSong: loadSong,
		lyrics:   lyrics,
	}
}

// lyricsService wraps a provider the way the running service does, with the
// context a live request carries.
func lyricsService(p *ytmProvider) *PlayerService {
	return &PlayerService{ytmProv: p, ctx: context.Background()}
}

// TestLyricsLocalTrackSkipsYTM pins the boundary: a local track never reaches
// the catalogue, even with a provider that would answer.
func TestLyricsLocalTrackSkipsYTM(t *testing.T) {
	calls := 0
	s := lyricsService(lyricProvider(
		func(context.Context, string) (*ytm.Song, error) {
			calls++

			return &ytm.Song{LyricsBrowseID: "MPLYt_x"}, nil
		},
		func(context.Context, string) (*ytm.Lyrics, error) {
			calls++

			return &ytm.Lyrics{}, nil
		},
	))

	doc, err := s.Lyrics("/music/album/01. Song.flac")
	if err != nil {
		t.Fatalf("Lyrics: %v", err)
	}
	if doc != nil {
		t.Fatalf("local track resolved to %+v, want nil", doc)
	}
	if calls != 0 {
		t.Fatalf("catalogue calls = %d, want 0 for a local track", calls)
	}
}

// TestLyricsYTMTrackResolvesSheet pins the map onto the document the player
// draws: start times become TimeMs and the instrumental markers are dropped.
func TestLyricsYTMTrackResolvesSheet(t *testing.T) {
	s := lyricsService(lyricProvider(
		func(context.Context, string) (*ytm.Song, error) {
			return &ytm.Song{ID: "abc123", LyricsBrowseID: "MPLYt_x"}, nil
		},
		func(_ context.Context, id string) (*ytm.Lyrics, error) {
			if id != "MPLYt_x" {
				t.Errorf("lyrics id = %q, want MPLYt_x", id)
			}

			return &ytm.Lyrics{Synced: true, Lines: []ytm.LyricLine{
				{Text: "♪", StartMs: 0, EndMs: 29000},
				{Text: "first line", StartMs: 29000, EndMs: 33000},
				{Text: "", StartMs: 34000},
				{Text: "second line", StartMs: 35000},
			}}, nil
		},
	))

	doc, err := s.Lyrics("ytm:abc123")
	if err != nil {
		t.Fatalf("Lyrics: %v", err)
	}
	if doc == nil {
		t.Fatal("no document for a catalogue track with lyrics")
	}
	if !doc.Synced {
		t.Error("sheet should be synced")
	}
	if doc.Source != "ytmusic" {
		t.Errorf("source = %q, want ytmusic", doc.Source)
	}
	if len(doc.Lines) != 2 {
		t.Fatalf("lines = %d, want 2 (markers dropped)", len(doc.Lines))
	}
	if doc.Lines[1].TimeMs != 35000 || doc.Lines[1].Text != "second line" {
		t.Errorf("line 1 = %+v", doc.Lines[1])
	}
}

// TestLyricsYTMUnavailableIsCached pins that a track without lyrics is not
// asked for twice: the second read must not spend a request.
func TestLyricsYTMUnavailableIsCached(t *testing.T) {
	requests := 0
	s := lyricsService(lyricProvider(
		func(context.Context, string) (*ytm.Song, error) {
			return &ytm.Song{ID: "abc123", LyricsBrowseID: "MPLYt_x"}, nil
		},
		func(context.Context, string) (*ytm.Lyrics, error) {
			requests++

			return nil, ytm.ErrLyricsNotAvailable
		},
	))

	for i := 0; i < 3; i++ {
		doc, err := s.Lyrics("ytm:abc123")
		if err != nil {
			t.Fatalf("Lyrics: %v", err)
		}
		if doc != nil {
			t.Fatalf("doc = %+v, want nil", doc)
		}
	}
	if requests != 1 {
		t.Fatalf("lyric requests = %d, want 1", requests)
	}
}

// TestLyricsYTMSongWithoutLyricsTabSkipsRequest pins the cheaper path: a track
// whose lyrics tab is absent costs one metadata call and no lyric request.
func TestLyricsYTMSongWithoutLyricsTabSkipsRequest(t *testing.T) {
	requests := 0
	s := lyricsService(lyricProvider(
		func(context.Context, string) (*ytm.Song, error) {
			return &ytm.Song{ID: "abc123"}, nil
		},
		func(context.Context, string) (*ytm.Lyrics, error) {
			requests++

			return &ytm.Lyrics{}, nil
		},
	))

	if doc, err := s.Lyrics("ytm:abc123"); err != nil || doc != nil {
		t.Fatalf("Lyrics = %+v, %v; want nil, nil", doc, err)
	}
	if requests != 0 {
		t.Fatalf("lyric requests = %d, want 0", requests)
	}
}

// TestLyricsYTMNetworkFailureIsNotAnError pins that a broken catalogue leaves
// the track playing with its empty lyric column instead of surfacing an error.
func TestLyricsYTMNetworkFailureIsNotAnError(t *testing.T) {
	s := lyricsService(lyricProvider(
		func(context.Context, string) (*ytm.Song, error) {
			return nil, errors.New("network down")
		},
		func(context.Context, string) (*ytm.Lyrics, error) {
			return nil, errors.New("network down")
		},
	))

	doc, err := s.Lyrics("ytm:abc123")
	if err != nil {
		t.Fatalf("Lyrics: %v", err)
	}
	if doc != nil {
		t.Fatalf("doc = %+v, want nil", doc)
	}
}

// TestLyricsWithoutProviderIsNil pins the degenerate case: a service built
// without the YouTube provider still answers for a streamed reference.
func TestLyricsWithoutProviderIsNil(t *testing.T) {
	doc, err := (&PlayerService{ctx: context.Background()}).Lyrics("ytm:abc123")
	if err != nil {
		t.Fatalf("Lyrics: %v", err)
	}
	if doc != nil {
		t.Fatalf("doc = %+v, want nil", doc)
	}
}

// TestRememberSheetBoundsTheCache pins the cap, which keeps a long session from
// holding a sheet per track played.
func TestRememberSheetBoundsTheCache(t *testing.T) {
	p := &ytmProvider{sheets: make(map[string]*ytm.Lyrics)}
	for i := 0; i < ytmSheetMax+10; i++ {
		p.rememberSheet(fmt.Sprintf("ytm:%d", i), &ytm.Lyrics{})
	}
	if len(p.sheets) > ytmSheetMax {
		t.Fatalf("cache holds %d sheets, cap is %d", len(p.sheets), ytmSheetMax)
	}
}
