// Persisted application settings for the desktop app.
//
// This file is the storage half of the settings panel: it reads and writes the
// small JSON document the panel edits, and translates it to and from the
// engine's own settings types. It is deliberately separate from the panel: the
// panel never touches the filesystem, and this file never touches the DOM.
//
// The store is a plain JSON file under the platform config directory, beside
// the user's effect scripts. The document carries the whole panel plus the
// Discord toggle, so one file holds every choice that survives a restart.
// JSON is the right size for a handful of scalar settings: no dependency,
// human-readable, and easy to repair by hand. The write is atomic (a temp file
// and a rename), which is the same discipline the waveform cache uses, so a
// crash mid-write cannot leave a half-written file that a later read would
// reject.
//
// The read is forgiving on purpose: a missing, unreadable, or corrupt file is
// not an error, it is "no settings yet", and the defaults are used. A settings
// file is a convenience, never a reason the app fails to start.

package backend

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/dlcuy22/molo"
	"github.com/dlcuy22/molow/internal/lyrics"
)

// settingsVersion is the schema version of the persisted file. It is written on
// every save and checked on load, so a future layout change can migrate an old
// file rather than misread it.
const settingsVersion = 1

// AppSettings is the persisted settings document. It is the whole file: a
// version tag, the settings grouped by the panel's tabs, and the Discord
// toggle alongside them. Only Playback has content today; the other groups are
// reserved so a later tab adds a field without changing the file's shape.
type AppSettings struct {
	Version  int              `json:"version"`
	Playback PlaybackSettings `json:"playback"`
	Stream   StreamSettings   `json:"stream"`
	General  GeneralSettings  `json:"general"`
	Discord  DiscordSettings  `json:"discord"`
}

// PlaybackSettings is the Playback tab. Crossfade is the track overlap; Repeat
// is the engine's repeat mode as the "off"/"all"/"one" strings, the same
// spelling the snapshot publishes. PlayPauseStyle chooses the dock's primary
// action: "default" is the inverted disc, "simple" is the bare album-player
// glyph. NoLyricsBehaviour chooses where the fullscreen player sits when a
// track has no lyrics: "stay" keeps the two-column layout, "center" centers the
// player. Lyrics is the lyric-provider group: the toggles (word-synced beats
// line-synced across the board, so there is no order to move), where a fetched
// sheet is written, and the folder the YouTube Music sidecar files live in.
type PlaybackSettings struct {
	Crossfade                CrossfadeSettings `json:"crossfade"`
	Repeat                   string            `json:"repeat"`
	PlayPauseStyle           string            `json:"playPauseStyle"`
	NoLyricsBehaviour        string            `json:"noLyricsBehaviour"`
	DisableEffectsLevelMeter bool              `json:"disableEffectsLevelMeter"`
	Lyrics                   LyricsSettings    `json:"lyrics"`
}

// LyricsSettings carries the lyric-provider configuration. Providers maps the
// chain names ("petitlyrics", "betterlyrics", "lyricsplus", "kugou", "lrclib")
// to their toggles, all on unless the user says otherwise. SaveLocation is
// "lyrics" (a lyrics/ folder in the track's own directory) or "beside" (the
// .lrc next to the file). Folder is where YouTube Music references cache their
// <videoId>.lrc; an empty value keeps the default under the config dir.
//
// AutoRomanization turns the romanized lyric tier on, and RomanizationLanguage
// picks which engine runs. "jp" is the only one with an engine; "ko", "zh" and
// "cjk" are reserved, and a value without an engine leaves the sheet untouched.
// AutoSpacing writes a word break between morphemes in the romaji, which reads
// like prose; off keeps the line joined the way the kana are written. It has no
// effect while AutoRomanization is off.
//
// WordSweep is which rows the karaoke highlight runs on: "both" sweeps the
// original and the romaji, "original" keeps the romaji a static reading line.
// The sweep is a display choice, so the backend only stores it; the pane reads
// it directly.
//
// PetitLyricsDelayMs shifts PetitLyrics timing in milliseconds (-2000 to 2000,
// default 0): negative moves late lyrics earlier, positive delays early ones.
// KaraokeStyle picks how word-synced lyrics highlight each word: "performance"
// fades word opacity on the compositor, while "quality" sweeps a left-to-right
// gradient across the word and animates instrumental note progress.
type LyricsSettings struct {
	Providers            map[string]bool `json:"providers"`
	PetitLyricsDelayMs   int             `json:"petitLyricsDelayMs"`
	SaveLocation         string          `json:"saveLocation"`
	Folder               string          `json:"folder"`
	AutoRomanization     bool            `json:"autoRomanization"`
	RomanizationLanguage string          `json:"romanizationLanguage"`
	AutoSpacing          bool            `json:"autoSpacing"`
	WordSweep            string          `json:"wordSweep"`
	KaraokeStyle         string          `json:"karaokeStyle"`
}

// StreamSettings is the Stream tab. TrackCacheMaxMB sets the on-disk budget in
// megabytes for cached online audio streams; 0 disables disk caching.
type StreamSettings struct {
	TrackCacheMaxMB int `json:"trackCacheMaxMb"`
}

// GeneralSettings is the General tab. VisualizerEnabled is the persisted
// on/off switch, the same one the v key flips and the panel's Off option
// writes. VisualizerStyle picks the album player layout, "full" or "minimal";
// off lives in VisualizerEnabled so the chosen layout survives a toggle.
// VisualizerDelayMs is the user's compensation in milliseconds: the visualizer
// taps the stream upstream of the device and driver latencies, so it leads the
// ear, and the user dials the remainder in to land the bars with the sound.
// LogLevel is the panel's "info" or "debug", the debug spelling being what a
// MOLOW_DEBUG run shows. LogFileEnabled is the Misc switch that mirrors the
// app's log to a file, and LogFile is where it lives; an empty value keeps the
// default molow.log in the config folder.
type GeneralSettings struct {
	VisualizerEnabled bool   `json:"visualizerEnabled"`
	VisualizerStyle   string `json:"visualizerStyle"`
	VisualizerDelayMs int    `json:"visualizerDelayMs"`
	LogLevel          string `json:"logLevel"`
	LogFileEnabled    bool   `json:"logFileEnabled"`
	LogFile           string `json:"logFile"`
}

// DiscordSettings carries the Discord Rich Presence toggle. Enabled is the
// persisted choice; whether the IPC socket is open is runtime state and is
// reported through DiscordStatus instead.
type DiscordSettings struct {
	Enabled bool `json:"enabled"`
}

// CrossfadeSettings mirrors the engine's crossfade settings. It is restated
// rather than reusing molo.CrossfadeSettings directly so the JSON field names
// are the panel's contract and a change to the engine's Go field names cannot
// silently re-shape the file.
type CrossfadeSettings struct {
	Enabled       bool `json:"enabled"`
	DurationMs    int  `json:"durationMs"`
	SkipSameAlbum bool `json:"skipSameAlbum"`
}

// defaultAppSettings is the state a fresh install starts from. Crossfade is off
// and takes the engine's reference values, so an untouched install behaves as
// it did before the feature existed. The visualizer defaults to on, which is the
// pump's own starting state. Discord presence defaults to off, so a fresh
// install never publishes presence.
func defaultAppSettings() AppSettings {
	return AppSettings{
		Version: settingsVersion,
		Playback: PlaybackSettings{
			Crossfade: CrossfadeSettings{
				Enabled:       false,
				DurationMs:    molo.DefaultCrossfadeMs,
				SkipSameAlbum: true,
			},
			Repeat:                   "off",
			PlayPauseStyle:           "default",
			NoLyricsBehaviour:        "stay",
			DisableEffectsLevelMeter: false,
			Lyrics:                   defaultLyricsSettings(),
		},
		Stream: StreamSettings{
			TrackCacheMaxMB: defaultTrackCacheMaxMB,
		},
		General: GeneralSettings{
			VisualizerEnabled: true,
			VisualizerStyle:   "minimal",
			LogLevel:          "info",
			// A small compensation by default: even a plain wired setup plays
			// a few tens of milliseconds after the tap, so 25 ms lands the bars
			// with the sound out of the box and the user only raises it for a
			// wireless device.
			VisualizerDelayMs: defaultVisualizerDelayMs,
		},
	}
}

// defaultLyricsSettings is the lyric-provider starting state: every provider
// on, sheets written to a lyrics/ folder, YouTube Music files in the config
// dir until the user picks a folder of their own.
func defaultLyricsSettings() LyricsSettings {
	providers := make(map[string]bool, len(lyrics.ChainProviders))
	for _, name := range lyrics.ChainProviders {
		providers[name] = true
	}

	return LyricsSettings{
		Providers:            providers,
		PetitLyricsDelayMs:   0,
		SaveLocation:         "lyrics",
		Folder:               "",
		AutoRomanization:     false,
		RomanizationLanguage: lyricRomanizationJP,
		AutoSpacing:          false,
		WordSweep:            lyricWordSweepBoth,
		KaraokeStyle:         "quality",
	}
}

// settingsPath is where the settings document lives. It follows the platform
// config convention, landing beside the effects folder.
func settingsPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(base, "molow", "settings.json"), nil
}

// loadAppSettings reads the persisted settings, falling back to the defaults
// for a missing, unreadable, or malformed file. It never returns an error: a
// settings file is a convenience, and a bad one must not stop the app.
//
// verb is the word the log line uses for the read: "loaded" on the startup
// read, "reloaded" for any later one. The path logged is the absolute path the
// app actually read.
func loadAppSettings(log *slog.Logger, verb string) AppSettings {
	def := defaultAppSettings()

	p, err := settingsPath()
	if err != nil {
		return def
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		log.Info("config: no settings file, using defaults", "path", p)

		return def
	}

	var s AppSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		log.Warn("config: unreadable, using defaults", "path", p, "error", err)

		return def
	}
	log.Info("config: "+verb, "path", p)

	return normalizeAppSettings(s, rawFieldPresence(raw))
}

type settingsFieldPresence struct {
	general map[string]bool
	stream  map[string]bool
}

// rawFieldPresence reports which General and Stream fields the raw document
// actually carried. A plain bool or zero int cannot tell "absent" from
// "explicitly false/0", and the two mean different things here: an absent
// visualizerEnabled or trackCacheMaxMb keeps the shipped default, while an
// explicit false or 0 is the user turning the feature off.
func rawFieldPresence(raw []byte) *settingsFieldPresence {
	var probe struct {
		General map[string]json.RawMessage `json:"general"`
		Stream  map[string]json.RawMessage `json:"stream"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil
	}
	general := make(map[string]bool, len(probe.General))
	for k := range probe.General {
		general[k] = true
	}
	stream := make(map[string]bool, len(probe.Stream))
	for k := range probe.Stream {
		stream[k] = true
	}

	return &settingsFieldPresence{general: general, stream: stream}
}

// normalizeAppSettings fills any field a file did not carry and clamps the
// crossfade duration into the engine's accepted range, so a hand-edited file
// cannot wedge the panel or send the engine a value it would only clamp again.
//
// present records which fields the raw file carried, and is nil on the save
// path. The loader passes the real presence set so fields omitted by older
// files keep their defaults, while a save from the panel carries a complete
// document, so a nil set trusts explicit false/0 values as sent.
func normalizeAppSettings(s AppSettings, present *settingsFieldPresence) AppSettings {
	def := defaultAppSettings()

	// A file written by an older schema may lack the version; treat it as the
	// current one rather than refusing it.
	s.Version = settingsVersion

	// A zero duration means the field was absent, not "instant"; fall back to
	// the default before clamping.
	if s.Playback.Crossfade.DurationMs == 0 {
		s.Playback.Crossfade.DurationMs = def.Playback.Crossfade.DurationMs
	}
	s.Playback.Crossfade.DurationMs = clampCrossfadeMs(s.Playback.Crossfade.DurationMs)

	// An empty repeat is the migration case: a file written before the field
	// existed. Both that and a hand-edit with an unknown value normalize to off
	// rather than reaching the engine with a value it would reject.
	if s.Playback.Repeat == "" {
		s.Playback.Repeat = def.Playback.Repeat
	} else if _, err := repeatFromName(s.Playback.Repeat); err != nil {
		s.Playback.Repeat = def.Playback.Repeat
	}

	// A file written before the control-style field existed gets the disc,
	// which is what the dock shipped with. Only the two known names are kept,
	// so a hand-edit cannot put the panel in a state with no selection.
	switch s.Playback.PlayPauseStyle {
	case "default", "simple":
	default:
		s.Playback.PlayPauseStyle = def.Playback.PlayPauseStyle
	}

	switch s.Playback.NoLyricsBehaviour {
	case "stay", "center":
	default:
		s.Playback.NoLyricsBehaviour = def.Playback.NoLyricsBehaviour
	}

	s.Playback.Lyrics = normalizeLyricsSettings(s.Playback.Lyrics, def.Playback.Lyrics)

	if present != nil && !present.stream["trackCacheMaxMb"] {
		s.Stream.TrackCacheMaxMB = def.Stream.TrackCacheMaxMB
	}
	s.Stream.TrackCacheMaxMB = clampTrackCacheMaxMB(s.Stream.TrackCacheMaxMB)

	// An install that predates the field keeps its visualizer on; only an
	// explicit false turns it off. On the save path present is nil and the value
	// the panel sent is taken as-is.
	if present != nil && !present.general["visualizerEnabled"] {
		s.General.VisualizerEnabled = def.General.VisualizerEnabled
	}

	// A file written before the style field existed, or a hand-edit with an
	// unknown value, gets the default layout. Off is not a style: it lives in
	// the enabled flag, so the chosen layout survives turning the visualizer
	// back on.
	switch s.General.VisualizerStyle {
	case "full", "minimal":
	default:
		s.General.VisualizerStyle = def.General.VisualizerStyle
	}

	// The delay is bounded on both ends: below zero has no meaning and above
	// the slider's top the backlog it asks for would outrun what the tap ring
	// can still serve. A file that predates the field gets the shipped default
	// rather than zero, the same way a predated visualizerEnabled keeps its
	// visualizer: absent is not the same as "explicitly none". On the save path
	// present is nil and the panel's value is taken as-is, so a user who dials
	// the slider to zero keeps it.
	if present != nil && !present.general["visualizerDelayMs"] {
		s.General.VisualizerDelayMs = def.General.VisualizerDelayMs
	}
	s.General.VisualizerDelayMs = clampVisualizerDelayMs(s.General.VisualizerDelayMs)
	// The level is the two names the panel offers; anything else is info,
	// which is what the app shipped logging at.
	switch s.General.LogLevel {
	case "info", "debug":
	default:
		s.General.LogLevel = "info"
	}
	// A pasted path often carries a trailing space from a copy, which would
	// otherwise name a file that is not the one the user asked for.
	s.General.LogFile = strings.TrimSpace(s.General.LogFile)

	return s
}

const (
	defaultTrackCacheMaxMB = 512
	maxTrackCacheMaxMB     = 4096
)

func clampTrackCacheMaxMB(mb int) int {
	switch {
	case mb < 0:
		return 0
	case mb > maxTrackCacheMaxMB:
		return maxTrackCacheMaxMB
	default:
		return mb
	}
}

// maxVisualizerDelayMs bounds the compensation. It sits well under the tap
// ring's ~680 ms, so the history a large setting asks the display to hold is
// always there, and it covers the slowest consumer codecs (Bluetooth sits
// around 200-300 ms) plus the desktop's own output latency with room to spare.
const maxVisualizerDelayMs = 500

// defaultVisualizerDelayMs is the shipped compensation. It is not zero: the
// desktop's own output chain (server quantum, driver, hardware) sits a few tens
// of milliseconds below the tap, so 25 ms already lands the bars with the sound
// on a wired setup; a wireless device raises it from there.
const defaultVisualizerDelayMs = 25

// clampVisualizerDelayMs bounds the visualizer's compensating delay to the
// slider's range.
func clampVisualizerDelayMs(ms int) int {
	switch {
	case ms < 0:
		return 0
	case ms > maxVisualizerDelayMs:
		return maxVisualizerDelayMs
	default:
		return ms
	}
}

const (
	minPetitLyricsDelayMs = -2000
	maxPetitLyricsDelayMs = 2000
)

func clampPetitLyricsDelayMs(ms int) int {
	switch {
	case ms < minPetitLyricsDelayMs:
		return minPetitLyricsDelayMs
	case ms > maxPetitLyricsDelayMs:
		return maxPetitLyricsDelayMs
	default:
		return ms
	}
}

// normalizeLyricsSettings drops unknown providers, defaults omitted ones to on,
// and falls back invalid save, language, or sweep values to def.
func normalizeLyricsSettings(s LyricsSettings, def LyricsSettings) LyricsSettings {
	out := LyricsSettings{
		Providers:          make(map[string]bool, len(lyrics.ChainProviders)),
		PetitLyricsDelayMs: clampPetitLyricsDelayMs(s.PetitLyricsDelayMs),
		AutoRomanization:   s.AutoRomanization,
		AutoSpacing:        s.AutoSpacing,
	}
	for _, name := range lyrics.ChainProviders {
		on, present := s.Providers[name]
		if !present {
			on = def.Providers[name]
		}
		out.Providers[name] = on
	}
	switch s.SaveLocation {
	case "lyrics", "beside":
		out.SaveLocation = s.SaveLocation
	default:
		out.SaveLocation = def.SaveLocation
	}
	switch s.RomanizationLanguage {
	case "jp":
		out.RomanizationLanguage = s.RomanizationLanguage
	default:
		out.RomanizationLanguage = def.RomanizationLanguage
	}
	switch s.WordSweep {
	case lyricWordSweepBoth, lyricWordSweepOriginal:
		out.WordSweep = s.WordSweep
	default:
		out.WordSweep = def.WordSweep
	}
	switch s.KaraokeStyle {
	case "performance", "quality":
		out.KaraokeStyle = s.KaraokeStyle
	default:
		out.KaraokeStyle = def.KaraokeStyle
	}
	out.Folder = s.Folder

	return out
}

// clampCrossfadeMs bounds a duration to the engine's range.
func clampCrossfadeMs(ms int) int {
	switch {
	case ms < molo.MinCrossfadeMs:
		return molo.MinCrossfadeMs
	case ms > molo.MaxCrossfadeMs:
		return molo.MaxCrossfadeMs
	default:
		return ms
	}
}

// saveAppSettings writes the settings document atomically. A write goes to a
// temporary file in the same directory and is renamed into place, so a reader
// never sees a partial file and a crash mid-write leaves the previous settings
// intact. The path logged is the absolute path the file landed at; a write
// failure is returned for the caller to warn about.
func saveAppSettings(log *slog.Logger, s AppSettings) error {
	s.Version = settingsVersion

	p, err := settingsPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)

		return err
	}
	// Flush to disk before the rename, so the rename cannot publish a file
	// whose contents are still only in the page cache.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)

		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)

		return err
	}
	// A settings file is readable by the user, not secret, and CreateTemp
	// makes 0600, so the mode is set explicitly.
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)

		return err
	}
	if err := os.Rename(name, p); err != nil {
		os.Remove(name)

		return err
	}
	log.Info("config: wrote", "path", p)

	return nil
}

// crossfadeFromSettings maps the persisted crossfade to the engine's type.
func crossfadeFromSettings(c CrossfadeSettings) molo.CrossfadeSettings {
	return molo.CrossfadeSettings{
		Enabled:       c.Enabled,
		DurationMs:    c.DurationMs,
		SkipSameAlbum: c.SkipSameAlbum,
	}
}

// crossfadeToSettings maps the engine's crossfade back to the persisted shape.
func crossfadeToSettings(c molo.CrossfadeSettings) CrossfadeSettings {
	return CrossfadeSettings{
		Enabled:       c.Enabled,
		DurationMs:    c.DurationMs,
		SkipSameAlbum: c.SkipSameAlbum,
	}
}

// AppSettings returns the persisted settings the panel renders. It is a plain
// read: the panel calls it once on open and mirrors edits locally.
func (s *PlayerService) AppSettings() AppSettings {
	s.appSettingsMu.Lock()
	defer s.appSettingsMu.Unlock()

	return s.appSettings
}

// SetAppSettings replaces the persisted settings, applies the playback half to
// the engine, and writes the document to disk. It returns the normalized
// document that was stored, so the caller can mirror exactly what was
// persisted rather than the value it sent (which may have been clamped).
//
// Applying first means a failed disk write still leaves the engine running what
// the user chose; the write is best effort, so a read-only config directory
// only costs persistence across a restart, never the setting in this session.
func (s *PlayerService) SetAppSettings(next AppSettings) (AppSettings, error) {
	// The panel always sends a whole document, so every field is present by
	// construction; a nil presence set means the normalizer treats the document
	// as fully specified.
	next = normalizeAppSettings(next, nil)

	s.appSettingsMu.Lock()
	s.appSettings = next
	s.appSettingsMu.Unlock()

	s.applyAppSettingsLocked()
	// Update dictionary residency on save rather than on the read path.
	s.syncRomanizer()
	// The log file follows the document on the same path the engine settings
	// do, so a toggle, a level or a new path takes effect on the save itself.
	s.applyLogSettings()
	s.applyTrackCacheSettings()
	s.emit(eventSettings, next)

	if err := saveAppSettings(s.log, next); err != nil {
		s.log.Warn("settings: could not persist", "error", err)

		return next, err
	}

	return next, nil
}

// persistRepeatMode updates the persisted repeat setting, broadcasts it to all
// windows, and saves settings.json on a best-effort basis so UI buttons,
// shortcuts, and OS media controls cannot drift from disk.
func (s *PlayerService) persistRepeatMode(mode string) {
	s.appSettingsMu.Lock()
	if s.appSettings.Playback.Repeat == mode {
		s.appSettingsMu.Unlock()

		return
	}
	s.appSettings.Playback.Repeat = mode
	next := s.appSettings
	s.appSettingsMu.Unlock()

	s.emit(eventSettings, next)
	if err := saveAppSettings(s.log, next); err != nil {
		s.log.Warn("settings: could not persist repeat", "error", err)
	}
}

// applyAppSettingsLocked pushes the persisted playback settings onto the
// engine. It is called at startup and after every save, so the engine's copy
// and the panel's copy cannot drift. It is safe with no player yet, which is
// the case in tests that build the service without an engine.
//
// Mute and shuffle are deliberately not persisted: mute is gain 0, which
// SetVolume already covers, and shuffle is a one-shot queue reorder a restart
// should not replay.
func (s *PlayerService) applyAppSettingsLocked() {
	if s.player == nil {
		return
	}

	s.appSettingsMu.Lock()
	cfg := s.appSettings.Playback
	visualizer := s.appSettings.General.VisualizerEnabled
	s.appSettingsMu.Unlock()

	_ = s.player.SetCrossfade(crossfadeFromSettings(cfg.Crossfade))
	if mode, err := repeatFromName(cfg.Repeat); err == nil {
		_ = s.player.SetRepeat(mode)
	}
	// Carry the live shape through so only Enabled changes: a save must not
	// reset bars, fft or the frequency range to the shipped defaults, so the
	// current shape is read back and echoed with the new enable flag.
	s.spectrumMu.Lock()
	shape := s.spectrumCfg
	s.spectrumMu.Unlock()
	_ = s.ConfigureSpectrum(SpectrumConfig{
		Bars:    shape.Bars,
		MinHz:   int(shape.MinHz),
		MaxHz:   int(shape.MaxHz),
		FFT:     shape.FFT,
		Enabled: visualizer,
	})
}

func (s *PlayerService) applyTrackCacheSettings() {
	if s.tracks == nil {
		return
	}
	s.appSettingsMu.Lock()
	mb := s.appSettings.Stream.TrackCacheMaxMB
	s.appSettingsMu.Unlock()

	before := s.tracks.Stats()
	nextBytes := int64(mb) << 20
	_ = s.tracks.SetMaxBytes(nextBytes)
	if before.MaxBytes != nextBytes {
		after := s.tracks.Stats()
		s.logger().Info("trackcache: limit updated", "maxMb", mb, "files", after.Count, "bytes", after.TotalBytes)
	}
}

// TrackCacheClear reports what ClearTrackCache removed: the cached track count
// and the bytes freed.
type TrackCacheClear struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// ClearTrackCache removes every cached online track file that is not currently
// open for playback.
func (s *PlayerService) ClearTrackCache() (TrackCacheClear, error) {
	if s.tracks == nil {
		return TrackCacheClear{}, nil
	}
	before := s.tracks.Stats()
	err := s.tracks.Clear()
	after := s.tracks.Stats()
	files := before.Count - after.Count
	if files < 0 {
		files = 0
	}
	freed := before.TotalBytes - after.TotalBytes
	if freed < 0 {
		freed = 0
	}
	s.logger().Info("trackcache: cleared", "files", files, "bytes", freed, "remaining", after.Count)

	return TrackCacheClear{Files: files, Bytes: freed}, err
}
