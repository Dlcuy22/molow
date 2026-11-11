package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dlcuy22/molow/internal/lyrics"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// When a track starts, the prefetch runs the chain in the background and
// writes the winning sheet to disk as a sidecar. A pane that opens before the
// chain finishes waits for it. A track with no result is remembered only for
// this session, so the next launch tries again.

// eventLyrics tells the frontend that a sheet was written for a path, so an
// open lyric pane reads it again.
const eventLyrics = "molo:lyrics"

// lyricsJoinWait is how long the lyric pane waits for a prefetch that is
// already running. It stays under the chain budget, so the wait either ends
// with the answer or with an empty pane until the event arrives.
const lyricsJoinWait = 8 * time.Second

// lyricsHTTP is the client every provider call uses. A provider answers with
// a few kilobytes, so a request that outlives the chain budget has nothing
// left worth waiting for.
var lyricsHTTP = &http.Client{Timeout: lyrics.ChainBudget + 2*time.Second}

// lyricsState is the in-flight and completed prefetch bookkeeping. mu guards
// everything: the fields are written by the snapshot pump and the prefetch
// goroutine, and read by the lyric-pane handler.
type lyricsState struct {
	mu sync.Mutex

	// jobPath and jobStop track the one running prefetch; a track change
	// cancels it through jobStop.
	jobPath string
	jobStop context.CancelFunc
	jobDone chan struct{}

	// donePath is the path of the last completed prefetch, and done its
	// result when the chain found one. It is nil for a chained miss, and the
	// missing set remembers that only for the session.
	donePath string
	done     *lyrics.Document
	missing  map[string]struct{}
}

// syncLyrics starts the prefetch for a track that just changed, unless one is
// already running for it, the answer is on disk, or the chain already missed
// it this session. The snapshot pump calls it, so it must never block; the
// disk check only looks in the two directories the loader reads.
func (s *PlayerService) syncLyrics(snap Snapshot) {
	log := s.logger()
	if snap.Path == "" || snap.Duration <= 0 {
		return
	}
	if snap.Title == "" && !isYTMRef(snap.Path) {
		// A local track queued but not yet tagged has nothing to search for;
		// the tags arrive with the same pump.
		return
	}

	s.lyrics.mu.Lock()
	defer s.lyrics.mu.Unlock()

	// The two answered states are checked first and cost nothing, so a track
	// reported again on the next tick does no disk work. Why a track was
	// skipped is already in the log from the first time.
	if s.lyrics.jobPath == snap.Path || s.lyrics.donePath == snap.Path {
		return
	}
	if _, missed := s.lyrics.missing[snap.Path]; missed {
		return
	}

	// Stop the previous track's chain. Its result no longer belongs to the
	// current track, and a fast skip should not leave requests running until
	// they time out.
	if s.lyrics.jobStop != nil {
		s.lyrics.jobStop()
	}

	if sidecar := s.sidecarPath(snap.Path); sidecar != "" {
		s.lyrics.donePath = snap.Path
		// The stale sheet must go, or a later join serves the previous
		// track's sheet as this one's.
		s.lyrics.done = nil
		log.Debug("lyrics: sidecar already on disk, no fetch", "path", sidecar)

		return
	}
	meta := lyrics.TrackMeta{
		Title:       snap.Title,
		Artist:      snap.Artist,
		Album:       snap.Album,
		DurationSec: int(snap.Duration / 1000),
	}
	var ref string
	if isYTMRef(snap.Path) {
		ref = ytmID(snap.Path)
	}
	cancel, done := startLyricsFetch(s, snap.Path, ref, meta)
	s.lyrics.jobPath = snap.Path
	s.lyrics.jobStop = cancel
	s.lyrics.jobDone = done
	log.Debug("lyrics: chain started", "path", snap.Path, "ref", ref,
		"title", meta.Title, "artist", meta.Artist)
}

// startLyricsFetch launches one chain run and returns the cancel and done
// channel together, so a caller cannot set up only half of them. The run does
// no engine work.
func startLyricsFetch(s *PlayerService, path, ref string, meta lyrics.TrackMeta) (context.CancelFunc, chan struct{}) {
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		log := s.logger()

		enabled := s.lyricProvidersEnabled()
		raw, source, err := lyrics.Fetch(ctx, lyricsHTTP, meta, enabled, log)

		doc := (*lyrics.Document)(nil)
		if err == nil {
			// The sheet is written in the same shape as every other sidecar,
			// so a restart and the user's editor both read one format. If the
			// save fails, only the session cache is lost.
			target := s.lyricsTarget(path, ref, meta)
			log.Debug("lyrics: saving the winner", "path", path, "provider", source, "target", target, "bytes", len(raw))
			if target != "" && lyrics.Write(target, raw) == nil {
				lines, synced, perr := lyrics.Parse(raw)
				if perr != nil {
					log.Warn("lyrics: written sheet does not parse", "target", target, "error", perr)
				} else {
					doc = &lyrics.Document{Lines: lines, Synced: synced, Source: target}
					log.Debug("lyrics: sidecar live", "target", target, "source", source, "lines", len(lines), "synced", synced)
				}
			} else {
				log.Warn("lyrics: sidecar write failed", "target", target, "provider", source)
			}
		} else {
			log.Debug("lyrics: chain missed", "path", path, "error", err)
		}
		if doc == nil {
			// For a reference, the catalogue's own sheet closes the chain. It
			// is not written as a sidecar, so the next session runs the chain
			// again.
			if tier, ok := s.ytmSheetTier(path); ok {
				doc = tier
				log.Debug("lyrics: catalogue tier", "path", path, "lines", len(tier.Lines), "synced", tier.Synced)
			}
		}

		s.lyrics.mu.Lock()
		if s.lyrics.jobPath == path {
			s.lyrics.jobPath = ""
			s.lyrics.jobStop = nil
			s.lyrics.jobDone = nil
		}
		if doc != nil {
			s.lyrics.donePath = path
			s.lyrics.done = doc
		} else if ctx.Err() == nil {
			// A cancelled chain is not a catalogue miss. The track change
			// killed it mid-flight, so the session keeps no negative entry
			// and the next play runs the chain again.
			s.lyrics.missingMark(path)
		}
		s.lyrics.mu.Unlock()

		if doc != nil {
			log.Debug("lyrics: sheet concluded", "path", path, "source", doc.Source)
			s.emit(eventLyrics, path)
		} else {
			log.Debug("lyrics: no sheet, session remembers the miss", "path", path)
		}
	}()

	return cancel, done
}

// sidecarPath returns the lyric file already on disk for path, or "" when the
// loader would start the chain. It follows the loader's own lookup, so a file
// that exists costs no network.
func (s *PlayerService) sidecarPath(path string) string {
	if isYTMRef(path) {
		return lyrics.IDPath(ytmID(path), s.lyricsDirs())
	}
	doc, err := lyrics.Load(path)
	if err != nil || doc == nil {
		return ""
	}

	return doc.Source
}

// lyricsTarget is where a fetched sheet is written. A local track follows the
// save-location setting; a YouTube Music reference always goes to the
// configured lyrics folder as <videoId>.lrc, the only place the loader can
// find it again.
func (s *PlayerService) lyricsTarget(path, ref string, meta lyrics.TrackMeta) string {
	if ref != "" {
		return filepath.Join(s.lyricsDir(), ref+".lrc")
	}

	// For a local track the sidecar is named after the audio file itself. If
	// it were named from the fetched metadata, an unlabeled rip like
	// "Track01.mp3" could never find it, and every play where the tags and the
	// file name disagree would rewrite it.
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if strings.TrimSpace(stem) == "" {
		return ""
	}

	dir := filepath.Dir(path)
	switch s.lyricsSaveLocation() {
	case "beside":
		return filepath.Join(dir, stem+".lrc")
	default:
		return filepath.Join(dir, "lyrics", stem+".lrc")
	}
}

func (s *PlayerService) lyricsSaveLocation() string {
	s.appSettingsMu.Lock()
	defer s.appSettingsMu.Unlock()

	return s.appSettings.Playback.Lyrics.SaveLocation
}

func (s *PlayerService) lyricProvidersEnabled() map[string]bool {
	s.appSettingsMu.Lock()
	cfg := s.appSettings.Playback.Lyrics
	s.appSettingsMu.Unlock()

	enabled := make(map[string]bool, len(cfg.Providers))
	for name, on := range cfg.Providers {
		enabled[name] = on
	}

	return enabled
}

// lyricsDir is the folder where YouTube Music sheets are cached: the
// configured one, or the default under the config dir. It is created the
// first time it is needed.
func (s *PlayerService) lyricsDir() (dir string) {
	s.appSettingsMu.Lock()
	cfg := s.appSettings.Playback.Lyrics
	s.appSettingsMu.Unlock()

	if cfg.Folder != "" {
		return cfg.Folder
	}
	if base, err := os.UserConfigDir(); err == nil {
		dir = filepath.Join(base, "molo", "lyrics")
	}
	if dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}

	return dir
}

// lyricsDirs is the search list the loader gets for a reference.
func (s *PlayerService) lyricsDirs() []string {
	if d := s.lyricsDir(); d != "" {
		return []string{d}
	}

	return nil
}

// PickLyricsFolder shows the native folder picker for the YouTube Music lyric
// cache. It queues nothing; storage happens through SetAppSettings.
func (s *PlayerService) PickLyricsFolder() (string, error) {
	app := application.Get()
	if app == nil {
		return "", fmt.Errorf("no application")
	}

	dir, err := app.Dialog.OpenFile().
		SetTitle("Choose a lyrics folder").
		CanChooseDirectories(true).
		CanChooseFiles(false).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}

	return dir, nil
}

// LyricsCacheClear reports what ClearLyricsCache removed: the sidecar count
// and their total size in bytes.
type LyricsCacheClear struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// ClearLyricsCache deletes the sidecars in the YouTube Music lyrics folder, so
// the next play fetches them again. Only .lrc files directly inside the folder
// go: the folder can be user-chosen, and a nested folder or an unrelated file
// there was not written by the cache.
func (s *PlayerService) ClearLyricsCache() (LyricsCacheClear, error) {
	removed := LyricsCacheClear{}
	dir := s.lyricsDir()
	if dir == "" {
		return removed, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return removed, err
	}

	var failures []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".lrc") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			failures = append(failures, err)
			continue
		}
		removed.Files++
		removed.Bytes += info.Size()
	}

	return removed, errors.Join(failures...)
}

// joinLyrics returns the prefetch's result, waiting for a chain that is still
// running and reporting one that finished after the pane stopped looking. The
// wait is bounded, and the event announces the sidecar whenever it arrives.
func (s *PlayerService) joinLyrics(path string) (*lyrics.Document, bool) {
	s.lyrics.mu.Lock()
	if s.lyrics.donePath == path {
		doc := s.lyrics.done
		s.lyrics.mu.Unlock()

		return doc, doc != nil
	}
	job := s.lyrics.jobPath == path
	ch := s.lyrics.jobDone
	s.lyrics.mu.Unlock()
	if !job {
		return nil, false
	}

	select {
	case <-ch:
	case <-time.After(lyricsJoinWait):
		return nil, false
	}

	s.lyrics.mu.Lock()
	defer s.lyrics.mu.Unlock()
	if s.lyrics.donePath == path {
		return s.lyrics.done, s.lyrics.done != nil
	}

	return nil, false
}

// missingMark records the session's negative cache for a path.
func (l *lyricsState) missingMark(path string) {
	if l.missing == nil {
		l.missing = map[string]struct{}{}
	}
	l.missing[path] = struct{}{}
}

// instrumentalMarks fills long silences and the track's tail in the sheet the
// pane draws. The marks stay in memory; the sidecar keeps the provider's text
// untouched. The duration is only known for the track that is playing.
func (s *PlayerService) instrumentalMarks(path string, doc *lyrics.Document) *lyrics.Document {
	var duration int64
	if s.player != nil {
		if snap := s.Snapshot(); snap.Path == path {
			duration = snap.Duration
		}
	}

	return &lyrics.Document{
		Lines:  lyrics.MarkInstrumental(doc.Lines, duration),
		Synced: doc.Synced,
		Source: doc.Source,
	}
}

// readLyrics is the disk half of the lyric pane, for local tracks and cached
// references alike.
func (s *PlayerService) readLyrics(path string) (*lyrics.Document, error) {
	if isYTMRef(path) {
		return lyrics.LoadRef(ytmID(path), s.lyricsDirs())
	}

	return lyrics.Load(path)
}
