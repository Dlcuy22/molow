package backend

import (
	"context"
	"errors"
	"strings"

	"github.com/dlcuy22/molow/internal/lyrics"
	ytm "github.com/dlcuy22/ytm-go"
)

// ytmSheetMax bounds the lyric sheet cache. A sheet is a few kilobytes of text,
// so the cap is about keeping the map from growing for a whole listening
// session, not about memory pressure.
const ytmSheetMax = 64

func ytmSheetKey(ref string) string {
	libID := ytmRefLibraryID(ref)
	vid := ytmID(ref)
	if vid == "" {
		return ref
	}
	return libID + ":" + vid
}

// lyricsFor resolves the lyric sheet of a YouTube Music reference.
//
// A track with no lyrics, and a lookup that fails, both report a missing sheet:
// lyrics are decoration, and an unreachable catalogue must not disturb a track
// that is playing. A nil sheet with true means "known to have none", which is
// cached so the answer is not requested twice.
func (p *ytmProvider) lyricsFor(ctx context.Context, ref string) (*ytm.Lyrics, bool) {
	if !isYTMRef(ref) {
		return nil, false
	}
	key := ytmSheetKey(ref)
	if sheet, ok := p.cachedSheet(key); ok {
		return sheet, sheet != nil
	}

	calls, err := p.callsFor(ref)
	if err != nil || calls.loadSong == nil || calls.lyrics == nil {
		return nil, false
	}

	song, err := calls.loadSong(ctx, ytmID(ref))
	if err != nil {
		p.logger().Debug("ytm: lyric lookup failed", "ref", ref, "error", err)

		return nil, false
	}
	if song.LyricsBrowseID == "" {
		p.rememberSheet(key, nil)

		return nil, false
	}

	sheet, err := calls.lyrics(ctx, song.LyricsBrowseID)
	if err != nil {
		if errors.Is(err, ytm.ErrLyricsNotAvailable) || errors.Is(err, ytm.ErrLyricsNotFound) {
			p.rememberSheet(key, nil)

			return nil, false
		}
		p.logger().Debug("ytm: lyric fetch failed", "ref", ref, "error", err)

		return nil, false
	}

	p.rememberSheet(key, sheet)

	return sheet, true
}

// ytmSheetTier is the fallback tier when no source router is installed (such as
// minimal unit test fixtures).
func (s *PlayerService) ytmSheetTier(path string) (*lyrics.Document, bool) {
	if !isYTMRef(path) || s.ytmProv == nil {
		return nil, false
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	sheet, ok := s.ytmProv.lyricsFor(ctx, path)
	if !ok || sheet == nil {
		return nil, false
	}

	return ytmLyricsDocument(sheet), true
}

// cachedSheet reads the cache. A nil sheet with true is a track known to have
// no lyrics.
func (p *ytmProvider) cachedSheet(key string) (*ytm.Lyrics, bool) {
	p.sheetMu.Lock()
	defer p.sheetMu.Unlock()

	sheet, ok := p.sheets[key]

	return sheet, ok
}

// rememberSheet records one lookup result, dropping the oldest entry once the
// cap is reached.
func (p *ytmProvider) rememberSheet(key string, sheet *ytm.Lyrics) {
	p.sheetMu.Lock()
	defer p.sheetMu.Unlock()

	if p.sheets == nil {
		p.sheets = make(map[string]*ytm.Lyrics)
	}
	if _, ok := p.sheets[key]; !ok {
		p.sheetAt = append(p.sheetAt, key)
	}
	p.sheets[key] = sheet

	for len(p.sheetAt) > ytmSheetMax {
		oldest := p.sheetAt[0]
		p.sheetAt = p.sheetAt[1:]
		delete(p.sheets, oldest)
	}
}

func (p *ytmProvider) clearSheetsForLibrary(libraryID string) {
	if p == nil || libraryID == "" {
		return
	}
	prefix := libraryID + ":"
	p.sheetMu.Lock()
	defer p.sheetMu.Unlock()

	var kept []string
	for _, key := range p.sheetAt {
		if strings.HasPrefix(key, prefix) {
			delete(p.sheets, key)
			continue
		}
		kept = append(kept, key)
	}
	p.sheetAt = kept
}
