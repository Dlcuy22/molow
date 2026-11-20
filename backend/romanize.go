package backend

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dlcuy22/molow/internal/lyrics"
	"github.com/dlcuy22/molow/internal/romanize"
)

const lyricRomanizationJP = "jp"

const (
	lyricWordSweepBoth     = "both"
	lyricWordSweepOriginal = "original"
)

// romanizationIdleTimeout delays dropping the dictionary so quick toggles do
// not trigger a full re-parse.
const romanizationIdleTimeout = 5 * time.Minute

// romanizer owns the lyric engine and its lazy-load / idle-unload lifecycle.
type romanizer struct {
	mu        sync.Mutex
	engine    *romanize.Engine
	loading   chan struct{}
	unloadAt  *time.Timer
	unloadGen uint64
	closed    bool
	cachePath string
	logf      func(msg string, args ...any)
	wants     func() bool
	// pinned is true once this process reads [ipa.Dict], which stays resident until exit.
	pinned bool
}

// romanizeLyrics returns a copy of doc with Romanized fields populated so the
// shared prefetch cache is never mutated.
func (s *PlayerService) romanizeLyrics(doc *lyrics.Document) *lyrics.Document {
	cfg := s.lyricsRomanization()
	if !cfg.enabled || cfg.lang != lyricRomanizationJP {
		return doc
	}

	engine := s.romanizer.ready()
	if engine == nil {
		return doc
	}
	opts := romanize.Options{Spacing: cfg.spacing}

	out := make([]lyrics.Line, len(doc.Lines))
	for i, line := range doc.Lines {
		out[i] = line
		if !romanize.HasJapanese(line.Text) {
			continue
		}
		words := make([]romanize.Word, len(line.Words))
		for j, w := range line.Words {
			words[j] = romanize.Word{Text: w.Text}
		}
		res := engine.RomanizeWith(line.Text, words, opts)
		out[i].Romanized = res.Line
		if !res.OK || len(res.Words) == 0 {
			continue
		}
		ws := make([]lyrics.Word, len(line.Words))
		copy(ws, line.Words)
		for j := range ws {
			ws[j].Romanized = res.Words[j]
		}
		out[i].Words = ws
	}

	return &lyrics.Document{Lines: out, Synced: doc.Synced, Source: doc.Source}
}

type romanizationMode struct {
	enabled bool
	lang    string
	spacing bool
}

func (s *PlayerService) lyricsRomanization() romanizationMode {
	s.appSettingsMu.Lock()
	defer s.appSettingsMu.Unlock()
	cfg := s.appSettings.Playback.Lyrics

	return romanizationMode{
		enabled: cfg.AutoRomanization,
		lang:    cfg.RomanizationLanguage,
		spacing: cfg.AutoSpacing,
	}
}

func (s *PlayerService) romanizationWanted() bool {
	cfg := s.lyricsRomanization()

	return cfg.enabled && cfg.lang == lyricRomanizationJP
}

// syncRomanizer aligns dictionary residency with the saved settings.
func (s *PlayerService) syncRomanizer() {
	if s.romanizationWanted() {
		s.romanizer.hold()

		return
	}
	s.romanizer.releaseAfter(romanizationIdleTimeout)
}

// ready returns the live engine, starting or joining an in-flight load if needed.
func (r *romanizer) ready() *romanize.Engine {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()

		return nil
	}
	if r.engine != nil {
		e := r.engine
		r.mu.Unlock()

		return e
	}
	start := r.loading == nil
	if start {
		r.loading = make(chan struct{})
	}
	pending := r.loading
	r.mu.Unlock()

	if start {
		go r.load(pending)
	}

	<-pending

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.engine
}

// hold cancels any pending unload and ensures a load is running without blocking.
func (r *romanizer) hold() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()

		return
	}
	if r.unloadAt != nil {
		r.unloadAt.Stop()
		r.unloadAt = nil
	}
	r.unloadGen++
	start := r.engine == nil && r.loading == nil
	if start {
		r.loading = make(chan struct{})
	}
	pending := r.loading
	r.mu.Unlock()

	if start {
		go r.load(pending)
	}
}

// releaseAfter schedules an idle unload after d, replacing any prior timer.
func (r *romanizer) releaseAfter(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armUnloadLocked(d, true)
}

func (r *romanizer) releaseIfUnarmed(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armUnloadLocked(d, false)
}

func (r *romanizer) armUnloadLocked(d time.Duration, replace bool) {
	if r.closed || (r.engine == nil && r.loading == nil) {
		return
	}
	if r.unloadAt != nil {
		if !replace {
			return
		}
		r.unloadAt.Stop()
	}
	r.unloadGen++
	gen := r.unloadGen
	r.unloadAt = time.AfterFunc(d, func() { r.unload(gen) })
}

// unload drops the engine when its timer generation is still current and
// settings no longer ask for romanization.
func (r *romanizer) unload(gen uint64) {
	// Read settings before r.mu to match the appSettingsMu -> r.mu lock order.
	wanted := r.wants != nil && r.wants()

	r.mu.Lock()
	if r.closed || r.unloadGen != gen {
		r.mu.Unlock()

		return
	}
	r.unloadAt = nil
	if wanted || r.loading != nil {
		r.mu.Unlock()

		return
	}
	e := r.engine
	r.engine = nil
	pinned := r.pinned
	r.mu.Unlock()

	if e != nil {
		e.Close()
		if r.logf != nil {
			if pinned {
				r.logf("romanize: engine released, dictionary stays pinned until exit")
			} else {
				r.logf("romanize: dictionary unloaded")
			}
		}
	}
}

func (r *romanizer) load(done chan struct{}) {
	e, err := r.open()
	if err != nil && r.logf != nil {
		r.logf("romanize: could not load dictionary", "error", err)
	}

	r.mu.Lock()
	if r.closed {
		r.loading = nil
		r.mu.Unlock()
		close(done)
		if e != nil {
			e.Close()
		}

		return
	}
	r.engine = e
	r.loading = nil
	r.mu.Unlock()

	close(done)

	if e != nil && r.logf != nil {
		r.logf("romanize: dictionary loaded")
	}
	// Re-check after publishing so a toggle-off during load still arms unload.
	if e != nil && r.wants != nil && !r.wants() {
		r.releaseIfUnarmed(romanizationIdleTimeout)
	}
}

// open loads from the owned cache file when valid; otherwise it falls back to
// the embedded dictionary (and writes the cache for future runs), setting pinned.
func (r *romanizer) open() (*romanize.Engine, error) {
	r.mu.Lock()
	pinned := r.pinned
	r.mu.Unlock()
	if pinned {
		// ipa.Dict is already resident in this process; reuse it instead of loading a second copy.
		return romanize.New(), nil
	}

	if r.cachePath != "" && !romanize.CacheValid(r.cachePath) {
		if err := romanize.WriteCache(r.cachePath); err != nil {
			if r.logf != nil {
				r.logf("romanize: cache unavailable, using the embedded dictionary", "error", err)
			}
		} else if err := romanize.RecordCacheStamp(r.cachePath); err != nil && r.logf != nil {
			r.logf("romanize: cache stamp", "error", err)
		}
		if r.logf != nil {
			r.logf("romanize: dictionary loaded from the embedded copy")
		}
		r.mu.Lock()
		r.pinned = true
		r.mu.Unlock()

		return romanize.New(), nil
	}

	if r.cachePath == "" {
		r.mu.Lock()
		r.pinned = true
		r.mu.Unlock()

		return romanize.New(), nil
	}

	e, err := romanize.Open(r.cachePath)
	if err != nil {
		_ = os.Remove(r.cachePath)
		_ = os.Remove(romanize.CacheStampPath(r.cachePath))
		if r.logf != nil {
			r.logf("romanize: cache unreadable, using the embedded dictionary", "error", err)
		}
		r.mu.Lock()
		r.pinned = true
		r.mu.Unlock()

		return romanize.New(), nil
	}

	return e, nil
}

func romanizeCachePath() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}

	return filepath.Join(base, "molow", "ipa.dict")
}

func newRomanizer(logf func(string, ...any)) *romanizer {
	return &romanizer{cachePath: romanizeCachePath(), logf: logf}
}

func (r *romanizer) shutdown() {
	r.mu.Lock()
	r.closed = true
	if r.unloadAt != nil {
		r.unloadAt.Stop()
		r.unloadAt = nil
	}
	r.unloadGen++
	e := r.engine
	r.engine = nil
	r.mu.Unlock()

	if e != nil {
		e.Close()
	}
}
