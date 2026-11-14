package backend

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/dlcuy22/molo"
)

// quietLog keeps the config log lines out of the test output.
var quietLog = slog.New(slog.DiscardHandler)

// TestAppSettingsDefaultsAreOffAndReference pins the fresh-install state the
// panel shows before anything is saved: crossfade off, the default duration,
// the same-album skip armed, repeat off, and the visualizer on.
func TestAppSettingsDefaultsAreOffAndReference(t *testing.T) {
	got := defaultAppSettings()

	if got.Version != settingsVersion {
		t.Fatalf("Version = %d, want %d", got.Version, settingsVersion)
	}
	if got.Playback.Crossfade.Enabled {
		t.Fatal("crossfade should default to off")
	}
	if got.Playback.Crossfade.DurationMs != molo.DefaultCrossfadeMs {
		t.Fatalf("duration = %d, want %d", got.Playback.Crossfade.DurationMs, molo.DefaultCrossfadeMs)
	}
	if !got.Playback.Crossfade.SkipSameAlbum {
		t.Fatal("skip same album should default to on")
	}
	if got.Playback.Repeat != "off" {
		t.Fatalf("repeat = %q, want off", got.Playback.Repeat)
	}
	if got.Playback.PlayPauseStyle != "default" {
		t.Fatalf("playPauseStyle = %q, want default", got.Playback.PlayPauseStyle)
	}
	if !got.General.VisualizerEnabled {
		t.Fatal("visualizer should default to on")
	}
	if got.General.VisualizerStyle != "minimal" {
		t.Fatalf("visualizerStyle = %q, want minimal", got.General.VisualizerStyle)
	}
	if got.General.VisualizerDelayMs != defaultVisualizerDelayMs {
		t.Fatalf("visualizerDelayMs = %d, want %d", got.General.VisualizerDelayMs, defaultVisualizerDelayMs)
	}
	if got.General.LogFileEnabled {
		t.Fatal("log file should default to off")
	}
	if got.General.LogFile != "" {
		t.Fatalf("log file path = %q, want the empty default", got.General.LogFile)
	}
}

// TestAppSettingsRoundTrips proves the panel's edits survive a restart, for
// every field including the repeat mode and the visualizer switch.
func TestAppSettingsRoundTrips(t *testing.T) {
	isolateConfig(t)

	// A fresh install reads the defaults with no file present.
	if got := loadAppSettings(quietLog, "loaded"); got.Playback.Crossfade != defaultAppSettings().Playback.Crossfade {
		t.Fatalf("fresh load = %+v, want the defaults", got.Playback.Crossfade)
	}

	want := AppSettings{
		Playback: PlaybackSettings{
			Crossfade:      CrossfadeSettings{Enabled: true, DurationMs: 8000, SkipSameAlbum: false},
			Repeat:         "one",
			PlayPauseStyle: "simple",
		},
		General: GeneralSettings{
			VisualizerEnabled: false,
			LogFileEnabled:    true,
			LogFile:           filepath.Join("somewhere", "molow.log"),
		},
	}
	if err := saveAppSettings(quietLog, want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := loadAppSettings(quietLog, "loaded")
	if got.Playback.Crossfade != want.Playback.Crossfade {
		t.Fatalf("loaded = %+v, want %+v", got.Playback.Crossfade, want.Playback.Crossfade)
	}
	if got.Playback.Repeat != want.Playback.Repeat {
		t.Fatalf("loaded repeat = %q, want %q", got.Playback.Repeat, want.Playback.Repeat)
	}
	if got.Playback.PlayPauseStyle != want.Playback.PlayPauseStyle {
		t.Fatalf("loaded playPauseStyle = %q, want %q", got.Playback.PlayPauseStyle, want.Playback.PlayPauseStyle)
	}
	if got.General.VisualizerEnabled != want.General.VisualizerEnabled {
		t.Fatalf("loaded visualizer = %v, want %v", got.General.VisualizerEnabled, want.General.VisualizerEnabled)
	}
	if got.General.LogFileEnabled != want.General.LogFileEnabled || got.General.LogFile != want.General.LogFile {
		t.Fatalf("loaded log file = %v %q, want %v %q",
			got.General.LogFileEnabled, got.General.LogFile,
			want.General.LogFileEnabled, want.General.LogFile)
	}
}

// TestLoadAppSettingsNormalizesAnOldSchemaFile proves a file written before
// repeat and the visualizer existed comes back with repeat off and the
// visualizer on, which is the current behaviour rather than a wedged panel.
func TestLoadAppSettingsNormalizesAnOldSchemaFile(t *testing.T) {
	isolateConfig(t)

	p, err := settingsPath()
	if err != nil {
		t.Fatalf("settingsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// A file from the schema that predates both fields.
	body := `{"version":1,"playback":{"crossfade":{"enabled":true,"durationMs":6000}}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := loadAppSettings(quietLog, "loaded")
	if got.Playback.Repeat != "off" {
		t.Fatalf("repeat = %q, want off for an old-schema file", got.Playback.Repeat)
	}
	if got.Playback.PlayPauseStyle != "default" {
		t.Fatalf("playPauseStyle = %q, want default for an old-schema file", got.Playback.PlayPauseStyle)
	}
	if !got.General.VisualizerEnabled {
		t.Fatal("visualizer = false, want on for a file that predates the field")
	}
}

// TestLoadAppSettingsNormalizesTheVisualizerStyle pins the style field: a
// known layout survives, while an unknown one or a missing one falls back to
// minimal. Off is not a style; it lives in the enabled flag.
func TestLoadAppSettingsNormalizesTheVisualizerStyle(t *testing.T) {
	isolateConfig(t)

	p, err := settingsPath()
	if err != nil {
		t.Fatalf("settingsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		body string
		want string
	}{
		{"known", `{"version":1,"general":{"visualizerEnabled":true,"visualizerStyle":"full"}}`, "full"},
		{"unknown", `{"version":1,"general":{"visualizerEnabled":true,"visualizerStyle":"sideways"}}`, "minimal"},
		{"missing", `{"version":1,"general":{"visualizerEnabled":true}}`, "minimal"},
	}
	for _, c := range cases {
		if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := loadAppSettings(quietLog, "loaded").General.VisualizerStyle; got != c.want {
			t.Fatalf("%s: style = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestLoadAppSettingsNormalizesAnUnknownRepeat proves a hand-edited repeat
// string is corrected on read, so the panel and the engine never see a value
// the engine would reject.
func TestLoadAppSettingsNormalizesAnUnknownRepeat(t *testing.T) {
	isolateConfig(t)

	p, err := settingsPath()
	if err != nil {
		t.Fatalf("settingsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"playback":{"crossfade":{"durationMs":6000},"repeat":"bogus"}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := loadAppSettings(quietLog, "loaded").Playback.Repeat; got != "off" {
		t.Fatalf("repeat = %q, want off for an unknown value", got)
	}
}

// TestLoadAppSettingsNormalizesAnUnknownPlayPauseStyle proves the style falls
// back to the default disc, so the panel always has a valid option selected.
func TestLoadAppSettingsNormalizesAnUnknownPlayPauseStyle(t *testing.T) {
	isolateConfig(t)

	p, err := settingsPath()
	if err != nil {
		t.Fatalf("settingsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"playback":{"crossfade":{"durationMs":6000},"playPauseStyle":"fancy"}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := loadAppSettings(quietLog, "loaded").Playback.PlayPauseStyle; got != "default" {
		t.Fatalf("playPauseStyle = %q, want default for an unknown value", got)
	}
}

// TestLoadAppSettingsToleratesCorruptFile keeps a hand-broken file from stopping
// the app: the defaults are used instead of an error.
func TestLoadAppSettingsToleratesCorruptFile(t *testing.T) {
	isolateConfig(t)

	p, err := settingsPath()
	if err != nil {
		t.Fatalf("settingsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := loadAppSettings(quietLog, "loaded"); got.Playback.Crossfade != defaultAppSettings().Playback.Crossfade {
		t.Fatalf("corrupt file did not fall back to defaults: %+v", got.Playback.Crossfade)
	}
}

// TestLoadAppSettingsClampsDuration proves a hand-edited out-of-range duration
// is corrected on read, so the panel and the engine never see a bad value.
func TestLoadAppSettingsClampsDuration(t *testing.T) {
	isolateConfig(t)

	p, err := settingsPath()
	if err != nil {
		t.Fatalf("settingsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"playback":{"crossfade":{"enabled":true,"durationMs":99999}}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := loadAppSettings(quietLog, "loaded").Playback.Crossfade.DurationMs; got != molo.MaxCrossfadeMs {
		t.Fatalf("duration = %d, want the clamp %d", got, molo.MaxCrossfadeMs)
	}
}

// TestLoadAppSettingsClampsVisualizerDelay pins the delay bounds and the
// absent-field rule: a hand-edited file past the slider's top falls to it, a
// negative one falls to no compensation, and a file that predates the field
// gets the shipped default rather than zero (absent is not "explicitly none").
func TestLoadAppSettingsClampsVisualizerDelay(t *testing.T) {
	isolateConfig(t)

	p, err := settingsPath()
	if err != nil {
		t.Fatalf("settingsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"above the top", `{"version":1,"general":{"visualizerDelayMs":9000}}`, maxVisualizerDelayMs},
		{"below zero", `{"version":1,"general":{"visualizerDelayMs":-5}}`, 0},
		{"absent field", `{"version":1,"general":{}}`, defaultVisualizerDelayMs},
	} {
		if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := loadAppSettings(quietLog, "loaded").General.VisualizerDelayMs; got != tc.want {
			t.Fatalf("%s: delay = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestSaveAppSettingsIsAtomic proves the temp-file dance leaves no partial file
// behind: after a save the directory holds the settings file and no stray temp.
func TestSaveAppSettingsIsAtomic(t *testing.T) {
	isolateConfig(t)

	if err := saveAppSettings(quietLog, defaultAppSettings()); err != nil {
		t.Fatalf("save: %v", err)
	}

	p, err := settingsPath()
	if err != nil {
		t.Fatalf("settingsPath: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("a temp file was left behind: %s", e.Name())
		}
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("settings file missing after save: %v", err)
	}
}

// TestSetAppSettingsReachesTheEngine proves the panel's save path applies the
// playback settings to the engine, not just to the file: the crossfade and the
// repeat mode are pushed through, and the document is persisted.
func TestSetAppSettingsReachesTheEngine(t *testing.T) {
	isolateConfig(t)

	svc := newPlayerService()
	fp := newFakePlayer()
	svc.player = fp

	next := defaultAppSettings()
	next.Playback.Crossfade = CrossfadeSettings{Enabled: true, DurationMs: 9000, SkipSameAlbum: false}
	next.Playback.Repeat = "all"
	stored, err := svc.SetAppSettings(next)
	if err != nil {
		t.Fatalf("SetAppSettings: %v", err)
	}
	if !samePlayback(stored.Playback, next.Playback) {
		t.Fatalf("SetAppSettings stored = %+v, want %+v", stored.Playback, next.Playback)
	}

	if !fp.called("crossfade") {
		t.Fatal("SetAppSettings did not push the crossfade to the engine")
	}
	if fp.Repeat() != molo.RepeatAll {
		t.Fatalf("repeat mode = %v, want RepeatAll", fp.Repeat())
	}

	// The document is persisted and reads back with the same values.
	got := loadAppSettings(quietLog, "loaded")
	if !samePlayback(got.Playback, next.Playback) {
		t.Fatalf("persisted playback = %+v, want %+v", got.Playback, next.Playback)
	}
}

// samePlayback compares the settings document field by field, since the
// lyric-provider map is not comparable with ==.
func samePlayback(a, b PlaybackSettings) bool {
	if a.Crossfade != b.Crossfade || a.Repeat != b.Repeat || a.PlayPauseStyle != b.PlayPauseStyle {
		return false
	}
	if a.Lyrics.SaveLocation != b.Lyrics.SaveLocation || a.Lyrics.Folder != b.Lyrics.Folder {
		return false
	}
	if len(a.Lyrics.Providers) != len(b.Lyrics.Providers) {
		return false
	}
	for name, on := range a.Lyrics.Providers {
		if b.Lyrics.Providers[name] != on {
			return false
		}
	}

	return true
}

// TestSetAppSettingsKeepsVisualizerShape proves a save toggles only the
// visualizer's Enabled: a non-default shape set beforehand must survive, since
// ConfigureSpectrum otherwise rebuilds from the shipped defaults.
func TestSetAppSettingsKeepsVisualizerShape(t *testing.T) {
	isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	want := SpectrumConfig{Bars: 64, MinHz: 120, MaxHz: 6000, FFT: 4096, Enabled: true}
	if err := svc.ConfigureSpectrum(want); err != nil {
		t.Fatalf("ConfigureSpectrum: %v", err)
	}

	next := defaultAppSettings()
	next.General.VisualizerEnabled = false
	if _, err := svc.SetAppSettings(next); err != nil {
		t.Fatalf("SetAppSettings: %v", err)
	}

	got := svc.SpectrumConfig()
	if got.Enabled {
		t.Fatal("Enabled = true after saving the visualizer off")
	}
	if got.Bars != want.Bars || got.MinHz != want.MinHz || got.MaxHz != want.MaxHz || got.FFT != want.FFT {
		t.Fatalf("shape = %+v, want the set shape %+v", got, want)
	}
}

// TestSetAppSettingsKeepsVisualizerOff proves the save path does not re-default
// the visualizer: a panel sending false must come back false and persist false,
// where the load path's "absent means on" rule must not apply.
func TestSetAppSettingsKeepsVisualizerOff(t *testing.T) {
	isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	next := defaultAppSettings()
	next.General.VisualizerEnabled = false
	stored, err := svc.SetAppSettings(next)
	if err != nil {
		t.Fatalf("SetAppSettings: %v", err)
	}
	if stored.General.VisualizerEnabled {
		t.Fatal("SetAppSettings returned visualizer on, want the false the panel sent")
	}

	persisted := loadAppSettings(quietLog, "loaded")
	if persisted.General.VisualizerEnabled {
		t.Fatal("SetAppSettings persisted visualizer on, want false")
	}
}

// TestSetAppSettingsReturnsTheNormalizedDocument proves a clamped value is
// echoed back rather than silently stored, so a caller can mirror the truth.
func TestSetAppSettingsReturnsTheNormalizedDocument(t *testing.T) {
	isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	next := defaultAppSettings()
	next.Playback.Crossfade = CrossfadeSettings{Enabled: true, DurationMs: 999999}
	stored, err := svc.SetAppSettings(next)
	if err != nil {
		t.Fatalf("SetAppSettings: %v", err)
	}
	if stored.Playback.Crossfade.DurationMs != molo.MaxCrossfadeMs {
		t.Fatalf("stored duration = %d, want the clamp %d", stored.Playback.Crossfade.DurationMs, molo.MaxCrossfadeMs)
	}
}

// TestCrossfadeMappingsRoundTrip pins the two translators between the persisted
// shape and the engine's, so a field rename cannot silently drop a setting.
func TestCrossfadeMappingsRoundTrip(t *testing.T) {
	c := CrossfadeSettings{Enabled: true, DurationMs: 3000, SkipSameAlbum: true}

	eng := crossfadeFromSettings(c)
	if eng.Enabled != c.Enabled || eng.DurationMs != c.DurationMs || eng.SkipSameAlbum != c.SkipSameAlbum {
		t.Fatalf("to engine = %+v, want %+v", eng, c)
	}

	back := crossfadeToSettings(eng)
	if back != c {
		t.Fatalf("round trip = %+v, want %+v", back, c)
	}
}

// TestLoadAppSettingsNormalizesTheLyricsGroup proves a file from before the
// lyric group gets the all-on defaults, unknown provider names are dropped and
// only the two save locations are kept.
func TestLoadAppSettingsNormalizesTheLyricsGroup(t *testing.T) {
	isolateConfig(t)

	p, err := settingsPath()
	if err != nil {
		t.Fatalf("settingsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// A pre-lyrics file gets the all-on defaults...
	if err := os.WriteFile(p, []byte(`{"version":1,"playback":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lyrics := loadAppSettings(quietLog, "loaded").Playback.Lyrics
	if len(lyrics.Providers) != 5 {
		t.Fatalf("providers = %v, want the five chain names", lyrics.Providers)
	}
	for _, name := range []string{"betterlyrics", "lyricsplus", "kugou", "lrclib"} {
		if !lyrics.Providers[name] {
			t.Fatalf("provider %q = off, want the all-on default", name)
		}
	}
	if lyrics.SaveLocation != "lyrics" {
		t.Fatalf("saveLocation = %q, want lyrics for an unknown value", lyrics.SaveLocation)
	}

	// ...a file with junk keeps the real toggles and drops nothing sane.
	if err := os.WriteFile(p, []byte(`{"version":1,"playback":{"lyrics":{"providers":{"betterlyrics":false,"mystery":true},"saveLocation":"desktop"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lyrics = loadAppSettings(quietLog, "loaded").Playback.Lyrics
	if lyrics.Providers["betterlyrics"] {
		t.Fatal("betterlyrics = on, want the saved off state")
	}
	if _, stray := lyrics.Providers["mystery"]; stray {
		t.Fatal("an unknown provider name survived normalization")
	}
	for _, name := range []string{"lyricsplus", "kugou", "lrclib"} {
		if !lyrics.Providers[name] {
			t.Fatalf("provider %q = off, want the default on", name)
		}
	}
	if lyrics.SaveLocation != "lyrics" {
		t.Fatalf("saveLocation = %q, want lyrics for an unknown value", lyrics.SaveLocation)
	}
	if lyrics.Folder != "" {
		t.Fatalf("folder = %q, want empty for the default", lyrics.Folder)
	}
}

// TestLyricsTogglePersists proves a written providers map reads back: one
// provider off survives the save.
func TestLyricsTogglePersists(t *testing.T) {
	isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	next := defaultAppSettings()
	next.Playback.Lyrics.Providers["kugou"] = false
	next.Playback.Lyrics.SaveLocation = "beside"
	if _, err := svc.SetAppSettings(next); err != nil {
		t.Fatalf("SetAppSettings: %v", err)
	}

	lyrics := loadAppSettings(quietLog, "loaded").Playback.Lyrics
	if lyrics.Providers["kugou"] {
		t.Fatal("kugou = on, want the saved off state")
	}
	if lyrics.Providers["lrclib"] != true {
		t.Fatal("lrclib = off, want the untouched on state")
	}
	if lyrics.SaveLocation != "beside" {
		t.Fatalf("saveLocation = %q, want beside", lyrics.SaveLocation)
	}
}
