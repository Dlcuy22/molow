package backend

import (
	"context"
	"errors"

	"github.com/dlcuy22/molow/internal/lyrics"
	ytm "github.com/dlcuy22/ytm-go"
)

// ytmSheetMax bounds the lyric sheet cache. A sheet is a few kilobytes of text,
// so the cap is about keeping the map from growing for a whole listening
// session, not about memory pressure.
const ytmSheetMax = 64

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
	if sheet, ok := p.cachedSheet(ref); ok {
		return sheet, sheet != nil
	}

	calls := p.calls()
	if calls.loadSong == nil || calls.lyrics == nil {
		return nil, false
	}

	song, err := calls.loadSong(ctx, ytmID(ref))
	if err != nil {
		p.logger().Debug("ytm: lyric lookup failed", "ref", ref, "error", err)

		return nil, false
	}
	if song.LyricsBrowseID == "" {
		p.rememberSheet(ref, nil)

		return nil, false
	}

	sheet, err := calls.lyrics(ctx, song.LyricsBrowseID)
	if err != nil {
		if errors.Is(err, ytm.ErrLyricsNotAvailable) || errors.Is(err, ytm.ErrLyricsNotFound) {
			p.rememberSheet(ref, nil)

			return nil, false
		}
		p.logger().Debug("ytm: lyric fetch failed", "ref", ref, "error", err)

		return nil, false
	}

	p.rememberSheet(ref, sheet)

	return sheet, true
}

// ytmSheetTier is the last tier of the chain for a YouTube Music reference:
// the catalogue's own sheet, mapped to the document shape. It is not written
// as a sidecar and not remembered as a find, so the next session runs the
// chain again.
func (s *PlayerService) ytmSheetTier(path string) (*lyrics.Document, bool) {
	if !isYTMRef(path) || s.ytmProv == nil {
		return nil, false
	}
	sheet, ok := s.ytmProv.lyricsFor(s.ctx, path)
	if !ok || sheet == nil {
		return nil, false
	}

	return ytmLyricsDocument(sheet), true
}

// cachedSheet reads the cache. A nil sheet with true is a track known to have
// no lyrics.
func (p *ytmProvider) cachedSheet(ref string) (*ytm.Lyrics, bool) {
	p.sheetMu.Lock()
	defer p.sheetMu.Unlock()

	sheet, ok := p.sheets[ref]

	return sheet, ok
}

// rememberSheet records one lookup result, dropping the oldest entry once the
// cap is reached.
func (p *ytmProvider) rememberSheet(ref string, sheet *ytm.Lyrics) {
	p.sheetMu.Lock()
	defer p.sheetMu.Unlock()

	if p.sheets == nil {
		p.sheets = make(map[string]*ytm.Lyrics)
	}
	if _, ok := p.sheets[ref]; !ok {
		p.sheetAt = append(p.sheetAt, ref)
	}
	p.sheets[ref] = sheet

	for len(p.sheetAt) > ytmSheetMax {
		oldest := p.sheetAt[0]
		p.sheetAt = p.sheetAt[1:]
		delete(p.sheets, oldest)
	}
}
