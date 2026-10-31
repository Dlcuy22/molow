package backend

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/dlcuy22/molo"
	"github.com/dlcuy22/molo/dsp"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Equalizer presets.
//
// A preset is a JSON file with the curve only: the band count and the ten
// frequency/gain/Q triples. The stage's bypass and input/output gains are not
// part of it, because applying a preset must not change the listener's volume
// or switch the effect off.
//
// Built-ins are embedded in the binary, the way bundled Lua effects are. They
// are read-only and always current: an upgrade can add or fix one without
// reconciling a copy on disk. User presets live in one file per preset under
// the platform config directory, so they can be edited, shared, and deleted
// without touching the built-ins.

//go:embed presets/equalizer/*.json
var bundledPresets embed.FS

const (
	presetVersion       = 1
	presetImplEqualizer = "equalizer-rbj"
	presetSourceBuiltin = "builtin"
	presetSourceUser    = "user"

	presetNameMax = 64

	presetMinFreq = 20.0
	presetMaxFreq = 20000.0
	presetMinGain = -20.0
	presetMaxGain = 20.0
	presetMinQ    = 0.1
	presetMaxQ    = 10.0
)

// EffectPresetInfo is one selectable preset on the wire. Values is keyed by
// the effect's own parameter names, so the frontend can compare it with the
// live stage without knowing how the file is shaped.
type EffectPresetInfo struct {
	Name   string         `json:"name"`
	Source string         `json:"source"`
	Values map[string]any `json:"values"`
}

// effectPresetFile is the on-disk shape. The field names are the file's
// contract: they are deliberately domain terms (frequency, gain, Q) rather than
// parameter keys, so a key rename cannot break a saved preset.
type effectPresetFile struct {
	Version     int              `json:"version"`
	Impl        string           `json:"impl"`
	Name        string           `json:"name"`
	ActiveBands int              `json:"activeBands"`
	Bands       []presetBandFile `json:"bands"`
}

type presetBandFile struct {
	Frequency float64 `json:"frequency"`
	Gain      float64 `json:"gain"`
	Q         float64 `json:"q"`
}

// presetStore is the user preset directory. It is a struct rather than a bare
// path so a test can point it at a temp directory without touching the real
// config.
type presetStore struct {
	dir string
}

// newPresetStore resolves the preset directory beside settings.json.
func newPresetStore() (*presetStore, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}

	return &presetStore{dir: filepath.Join(base, "molo", "presets", "equalizer")}, nil
}

// EffectPresets lists the built-ins followed by the user's presets. An effect
// with no preset support returns an empty list rather than an error, so a
// caller can render the menu either way.
func (s *PlayerService) EffectPresets(impl string) ([]EffectPresetInfo, error) {
	if impl != presetImplEqualizer {
		return []EffectPresetInfo{}, nil
	}

	builtins, err := loadBundledPresets()
	if err != nil {
		return nil, err
	}
	out := make([]EffectPresetInfo, 0, len(builtins))
	for _, f := range builtins {
		out = append(out, presetInfo(f, presetSourceBuiltin))
	}

	store, err := newPresetStore()
	if err != nil {
		return nil, err
	}
	user, err := store.list()
	if err != nil {
		return nil, err
	}
	for _, f := range user {
		out = append(out, presetInfo(f, presetSourceUser))
	}

	return out, nil
}

// SaveEffectPreset writes the live stage's curve as a user preset. The file is
// replaced when the name already maps to the same slug; the UI asks before it
// gets here.
func (s *PlayerService) SaveEffectPreset(stageID, name string) (EffectPresetInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return EffectPresetInfo{}, fmt.Errorf("preset name is empty")
	}
	if len([]rune(name)) > presetNameMax {
		return EffectPresetInfo{}, fmt.Errorf("preset name is longer than %d characters", presetNameMax)
	}

	fx, err := s.effects()
	if err != nil {
		return EffectPresetInfo{}, err
	}
	st, err := findEffectStage(fx.EffectChain(), stageID)
	if err != nil {
		return EffectPresetInfo{}, err
	}

	f, err := presetFileFromStage(name, st)
	if err != nil {
		return EffectPresetInfo{}, err
	}
	if err := f.validate(); err != nil {
		return EffectPresetInfo{}, err
	}

	store, err := newPresetStore()
	if err != nil {
		return EffectPresetInfo{}, err
	}
	if err := store.save(f); err != nil {
		return EffectPresetInfo{}, err
	}

	return presetInfo(f, presetSourceUser), nil
}

// ApplyEffectPreset installs a preset on one stage. The bands are set before
// the band count so the curve is already in place when the extra bands become
// active, and the whole loop runs inside one command, which emits one snapshot
// instead of one per parameter.
func (s *PlayerService) ApplyEffectPreset(stageID, source, name string) error {
	f, err := loadPreset(source, name)
	if err != nil {
		return err
	}
	values := presetFileValues(f)

	fx, err := s.effects()
	if err != nil {
		return err
	}

	return s.command(func() error {
		for i, band := range f.Bands {
			for _, kv := range []struct {
				key   string
				value float64
			}{
				{dsp.EqualizerBandFreqKey(i), band.Frequency},
				{dsp.EqualizerBandGainKey(i), band.Gain},
				{dsp.EqualizerBandQKey(i), band.Q},
			} {
				if err := fx.SetEffectParam(stageID, kv.key, kv.value); err != nil {
					return err
				}
			}
		}

		return fx.SetEffectParam(stageID, dsp.EqualizerNumBands, values["num-bands"])
	})
}

// DeleteEffectPreset removes a user preset. Built-ins are never in the store
// directory, so they cannot be reached here.
func (s *PlayerService) DeleteEffectPreset(name string) error {
	store, err := newPresetStore()
	if err != nil {
		return err
	}

	return store.delete(name)
}

// OpenEffectPresetFolder creates the preset directory if it does not exist and
// opens it in the platform file manager.
func (s *PlayerService) OpenEffectPresetFolder() error {
	store, err := newPresetStore()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(store.dir, 0o755); err != nil {
		return err
	}
	app := application.Get()
	if app == nil {
		return fmt.Errorf("no application")
	}

	return app.Browser.OpenURL((&url.URL{Scheme: "file", Path: store.dir}).String())
}

// presetInfo maps a file to the wire shape.
func presetInfo(f effectPresetFile, source string) EffectPresetInfo {
	return EffectPresetInfo{Name: f.Name, Source: source, Values: presetFileValues(f)}
}

// presetFileValues maps the domain file to parameter keys. The keys are built
// by the engine's own helpers, so a future key change cannot leave the file and
// the engine disagreeing.
func presetFileValues(f effectPresetFile) map[string]any {
	out := make(map[string]any, 1+len(f.Bands)*3)
	out[dsp.EqualizerNumBands] = f.ActiveBands
	for i, band := range f.Bands {
		out[dsp.EqualizerBandFreqKey(i)] = band.Frequency
		out[dsp.EqualizerBandGainKey(i)] = band.Gain
		out[dsp.EqualizerBandQKey(i)] = band.Q
	}

	return out
}

// presetFileFromStage reads a stage's own parameters back into the file shape.
func presetFileFromStage(name string, st molo.EffectStage) (effectPresetFile, error) {
	if st.Impl != presetImplEqualizer {
		return effectPresetFile{}, fmt.Errorf("presets are not supported for %s", st.Impl)
	}

	active, ok := presetNumber(st.Values[dsp.EqualizerNumBands])
	if !ok {
		return effectPresetFile{}, fmt.Errorf("stage %s has no band count", st.ID)
	}
	count := int(math.Round(active))
	if count < 1 {
		count = 1
	}
	if count > dsp.EqualizerBandCount {
		count = dsp.EqualizerBandCount
	}
	bands := make([]presetBandFile, 0, count)
	for i := 0; i < count; i++ {
		freq, ok := presetNumber(st.Values[dsp.EqualizerBandFreqKey(i)])
		if !ok {
			return effectPresetFile{}, fmt.Errorf("stage %s has no frequency for band %d", st.ID, i+1)
		}
		gain, ok := presetNumber(st.Values[dsp.EqualizerBandGainKey(i)])
		if !ok {
			return effectPresetFile{}, fmt.Errorf("stage %s has no gain for band %d", st.ID, i+1)
		}
		q, ok := presetNumber(st.Values[dsp.EqualizerBandQKey(i)])
		if !ok {
			return effectPresetFile{}, fmt.Errorf("stage %s has no Q for band %d", st.ID, i+1)
		}
		bands = append(bands, presetBandFile{Frequency: freq, Gain: gain, Q: q})
	}

	f := effectPresetFile{
		Version:     presetVersion,
		Impl:        presetImplEqualizer,
		Name:        name,
		ActiveBands: count,
		Bands:       bands,
	}

	return f, f.validate()
}

// presetNumber accepts the numeric shapes the engine's parameter store uses.
func presetNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case float32:
		f := float64(n)

		return f, !math.IsNaN(f) && !math.IsInf(f, 0)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// validate checks the file contract and clamps the values a hand edit could
// push outside the effect's ranges. A file that cannot be represented is an
// error; a value that is merely out of range is repaired.
func (f *effectPresetFile) validate() error {
	if f.Version != presetVersion {
		return fmt.Errorf("preset version %d is not supported", f.Version)
	}
	if f.Impl != presetImplEqualizer {
		return fmt.Errorf("preset is for %q, not %q", f.Impl, presetImplEqualizer)
	}
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" {
		return fmt.Errorf("preset name is empty")
	}
	if len([]rune(f.Name)) > presetNameMax {
		return fmt.Errorf("preset name is longer than %d characters", presetNameMax)
	}
	if len(f.Bands) < 1 || len(f.Bands) > dsp.EqualizerBandCount {
		return fmt.Errorf("preset has %d bands, want 1 to %d", len(f.Bands), dsp.EqualizerBandCount)
	}
	if f.ActiveBands < 1 {
		f.ActiveBands = 1
	}
	if f.ActiveBands > len(f.Bands) {
		f.ActiveBands = len(f.Bands)
	}
	for i := range f.Bands {
		b := &f.Bands[i]
		if !finite(b.Frequency) || !finite(b.Gain) || !finite(b.Q) {
			return fmt.Errorf("preset band %d has a non-finite value", i+1)
		}
		b.Frequency = clampFloat(b.Frequency, presetMinFreq, presetMaxFreq)
		b.Gain = clampFloat(b.Gain, presetMinGain, presetMaxGain)
		b.Q = clampFloat(b.Q, presetMinQ, presetMaxQ)
	}

	return nil
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func clampFloat(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}

	return v
}

// loadBundledPresets decodes the embedded files in file-name order, which is
// the order the menu shows. A broken built-in is a build problem, so it is an
// error rather than a skipped file.
func loadBundledPresets() ([]effectPresetFile, error) {
	entries, err := fs.ReadDir(bundledPresets, "presets/equalizer")
	if err != nil {
		return nil, err
	}
	out := make([]effectPresetFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := bundledPresets.ReadFile("presets/equalizer/" + e.Name())
		if err != nil {
			return nil, err
		}
		var f effectPresetFile
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("preset %s: %w", e.Name(), err)
		}
		if err := f.validate(); err != nil {
			return nil, fmt.Errorf("preset %s: %w", e.Name(), err)
		}
		out = append(out, f)
	}

	return out, nil
}

// loadPreset resolves one source/name pair. The user source reads the file
// again instead of trusting a stale list, so a preset edited on disk between
// the menu read and the click is applied as it is now.
func loadPreset(source, name string) (effectPresetFile, error) {
	name = strings.TrimSpace(name)
	switch source {
	case presetSourceBuiltin:
		builtins, err := loadBundledPresets()
		if err != nil {
			return effectPresetFile{}, err
		}
		for _, f := range builtins {
			if f.Name == name {
				return f, nil
			}
		}
	case presetSourceUser:
		store, err := newPresetStore()
		if err != nil {
			return effectPresetFile{}, err
		}
		user, err := store.list()
		if err != nil {
			return effectPresetFile{}, err
		}
		for _, f := range user {
			if f.Name == name {
				return f, nil
			}
		}
	default:
		return effectPresetFile{}, fmt.Errorf("unknown preset source %q", source)
	}

	return effectPresetFile{}, fmt.Errorf("preset %q not found", name)
}

// list reads every decodable user preset. A corrupt file is logged and skipped:
// one bad edit must not hide the rest of the menu.
func (st *presetStore) list() ([]effectPresetFile, error) {
	entries, err := os.ReadDir(st.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, err
	}
	out := make([]effectPresetFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(st.dir, e.Name()))
		if err != nil {
			slog.Warn("preset: read", "file", e.Name(), "error", err)

			continue
		}
		var f effectPresetFile
		if err := json.Unmarshal(data, &f); err != nil {
			slog.Warn("preset: decode", "file", e.Name(), "error", err)

			continue
		}
		if err := f.validate(); err != nil {
			slog.Warn("preset: invalid", "file", e.Name(), "error", err)

			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	return out, nil
}

// save writes one preset atomically. A temp file in the same directory is
// flushed and renamed, so a crash cannot leave a half-written preset.
func (st *presetStore) save(f effectPresetFile) error {
	if err := os.MkdirAll(st.dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(st.dir, ".preset-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)

		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)

		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)

		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)

		return err
	}

	return os.Rename(name, filepath.Join(st.dir, presetFileName(f.Name)))
}

// delete removes a user preset by its display name.
func (st *presetStore) delete(name string) error {
	path := filepath.Join(st.dir, presetFileName(name))
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("preset %q does not exist", name)
		}

		return err
	}

	return nil
}

// presetFileName turns a display name into a safe file name. Only letters,
// digits and single dashes survive, so a name can never escape the directory.
func presetFileName(name string) string {
	slug := presetSlug(name)

	return slug + ".json"
}

func presetSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len([]rune(slug)) > 64 {
		slug = strings.TrimRight(string([]rune(slug)[:64]), "-")
	}
	if slug == "" {
		slug = "preset"
	}

	return slug
}

// findEffectStage locates one stage in the chain by the ID the editor holds.
func findEffectStage(chain molo.EffectChain, id string) (molo.EffectStage, error) {
	for _, st := range chain.Stages {
		if st.ID == id {
			return st, nil
		}
	}

	return molo.EffectStage{}, fmt.Errorf("no effect stage %q", id)
}
