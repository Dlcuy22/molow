package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlcuy22/molo"
	"github.com/dlcuy22/molo/dsp"
)

func eqValues() dsp.Values {
	v := dsp.Values{dsp.EqualizerNumBands: 10}
	for i := 0; i < presetBandCount; i++ {
		v[dsp.EqualizerBandFreqKey(i)] = 32.0 * float64(i+1)
		v[dsp.EqualizerBandGainKey(i)] = float64(i) - 5
		v[dsp.EqualizerBandQKey(i)] = 1.5
	}

	return v
}

func eqStage(values dsp.Values) molo.EffectStage {
	return molo.EffectStage{ID: "eq-1", Impl: presetImplEqualizer, Values: values}
}

func TestPresetSlug(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Vocal presence", "vocal-presence"},
		{"  Bass  Boost!  ", "bass-boost"},
		{"A/B test", "a-b-test"},
		{"", "preset"},
		{"!!!", "preset"},
		{"../../etc/passwd", "etc-passwd"},
		{"Telephone 2", "telephone-2"},
	}
	for _, tt := range tests {
		if got := presetSlug(tt.in); got != tt.want {
			t.Errorf("presetSlug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPresetSlugStaysInsideTheDirectory(t *testing.T) {
	name := presetFileName("../../escape")
	if strings.ContainsAny(name, `/\`) {
		t.Fatalf("presetFileName(%q) = %q, want no separators", "../../escape", name)
	}
}

func TestPresetSlugCapsLength(t *testing.T) {
	got := presetSlug(strings.Repeat("a", 200))
	if len([]rune(got)) > 64 {
		t.Fatalf("slug length = %d, want <= 64", len([]rune(got)))
	}
}

func TestPresetFileFromStage(t *testing.T) {
	f, err := presetFileFromStage("My curve", eqStage(eqValues()))
	if err != nil {
		t.Fatalf("presetFileFromStage: %v", err)
	}
	if f.Name != "My curve" || f.ActiveBands != 10 || len(f.Bands) != presetBandCount {
		t.Fatalf("file = %+v", f)
	}
	if f.Bands[0].Frequency != 32 || f.Bands[0].Gain != -5 || f.Bands[0].Q != 1.5 {
		t.Fatalf("band 0 = %+v", f.Bands[0])
	}
	if f.Bands[9].Frequency != 320 || f.Bands[9].Gain != 4 {
		t.Fatalf("band 9 = %+v", f.Bands[9])
	}
}

func TestPresetFileFromStageRejectsOtherEffects(t *testing.T) {
	st := molo.EffectStage{ID: "x", Impl: "compressor", Values: eqValues()}
	if _, err := presetFileFromStage("nope", st); err == nil {
		t.Fatal("presetFileFromStage accepted a compressor")
	}
}

func TestPresetFileFromStageRejectsMissingBands(t *testing.T) {
	values := eqValues()
	delete(values, dsp.EqualizerBandGainKey(3))
	if _, err := presetFileFromStage("broken", eqStage(values)); err == nil {
		t.Fatal("presetFileFromStage accepted a stage without band 4 gain")
	}
}

func TestPresetValidate(t *testing.T) {
	base := func() effectPresetFile {
		f, err := presetFileFromStage("Preset", eqStage(eqValues()))
		if err != nil {
			t.Fatalf("presetFileFromStage: %v", err)
		}

		return f
	}

	tests := []struct {
		name    string
		mutate  func(*effectPresetFile)
		wantErr bool
	}{
		{"ok", func(*effectPresetFile) {}, false},
		{"version", func(f *effectPresetFile) { f.Version = 99 }, true},
		{"impl", func(f *effectPresetFile) { f.Impl = "other" }, true},
		{"name", func(f *effectPresetFile) { f.Name = "   " }, true},
		{"band count", func(f *effectPresetFile) { f.Bands = f.Bands[:9] }, true},
		{"clamps", func(f *effectPresetFile) {
			f.Bands[0].Gain = 99
			f.Bands[1].Frequency = 1
			f.Bands[2].Q = 0
			f.ActiveBands = 99
		}, false},
	}
	for _, tt := range tests {
		f := base()
		tt.mutate(&f)
		err := f.validate()
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: validate() err = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
		if err == nil && tt.name == "clamps" {
			if f.Bands[0].Gain != presetMaxGain {
				t.Errorf("gain not clamped: %v", f.Bands[0].Gain)
			}
			if f.Bands[1].Frequency != presetMinFreq {
				t.Errorf("frequency not clamped: %v", f.Bands[1].Frequency)
			}
			if f.Bands[2].Q != presetMinQ {
				t.Errorf("Q not clamped: %v", f.Bands[2].Q)
			}
			if f.ActiveBands != presetBandCount {
				t.Errorf("active bands not clamped: %d", f.ActiveBands)
			}
		}
	}
}

func TestPresetFileValuesUsesEngineKeys(t *testing.T) {
	f, err := presetFileFromStage("Preset", eqStage(eqValues()))
	if err != nil {
		t.Fatalf("presetFileFromStage: %v", err)
	}
	v := presetFileValues(f)
	if v[dsp.EqualizerNumBands] != 10 {
		t.Fatalf("num-bands = %v", v[dsp.EqualizerNumBands])
	}
	if v[dsp.EqualizerBandFreqKey(0)] != 32.0 {
		t.Fatalf("band 0 frequency = %v", v[dsp.EqualizerBandFreqKey(0)])
	}
	if v[dsp.EqualizerBandGainKey(9)] != 4.0 {
		t.Fatalf("band 9 gain = %v", v[dsp.EqualizerBandGainKey(9)])
	}
}

func TestPresetStoreRoundTrip(t *testing.T) {
	store := &presetStore{dir: filepath.Join(t.TempDir(), "presets")}
	f, err := presetFileFromStage("Vocal presence", eqStage(eqValues()))
	if err != nil {
		t.Fatalf("presetFileFromStage: %v", err)
	}
	if err := store.save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	list, err := store.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "Vocal presence" {
		t.Fatalf("list = %+v", list)
	}

	if err := store.delete("vocal presence"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err = store.list()
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list after delete = %+v", list)
	}
}

func TestPresetStoreListSkipsBadFiles(t *testing.T) {
	dir := t.TempDir()
	store := &presetStore{dir: dir}
	f, err := presetFileFromStage("Good", eqStage(eqValues()))
	if err != nil {
		t.Fatalf("presetFileFromStage: %v", err)
	}
	if err := store.save(f); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write broken: %v", err)
	}
	if err := store.save(effectPresetFile{Version: 99, Impl: presetImplEqualizer, Name: "Old"}); err != nil {
		t.Fatalf("save invalid: %v", err)
	}

	list, err := store.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "Good" {
		t.Fatalf("list = %+v, want only Good", list)
	}
}

func TestPresetStoreListMissingDirectory(t *testing.T) {
	store := &presetStore{dir: filepath.Join(t.TempDir(), "absent")}
	list, err := store.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list != nil {
		t.Fatalf("list = %+v, want nil", list)
	}
}

func TestPresetStoreDeleteMissing(t *testing.T) {
	store := &presetStore{dir: t.TempDir()}
	if err := store.delete("nope"); err == nil {
		t.Fatal("delete accepted a missing preset")
	}
}

func TestBundledPresets(t *testing.T) {
	list, err := loadBundledPresets()
	if err != nil {
		t.Fatalf("loadBundledPresets: %v", err)
	}
	if len(list) < 5 {
		t.Fatalf("got %d built-ins, want at least 5", len(list))
	}
	if list[0].Name != "Flat" {
		t.Fatalf("first built-in = %q, want Flat", list[0].Name)
	}
	seen := make(map[string]bool, len(list))
	for _, f := range list {
		if seen[f.Name] {
			t.Errorf("duplicate built-in name %q", f.Name)
		}
		seen[f.Name] = true
		if len(f.Bands) != presetBandCount {
			t.Errorf("%s has %d bands", f.Name, len(f.Bands))
		}
		if f.ActiveBands < 1 || f.ActiveBands > presetBandCount {
			t.Errorf("%s has active bands %d", f.Name, f.ActiveBands)
		}
	}
}

func TestLoadPresetUnknownSource(t *testing.T) {
	if _, err := loadPreset("disk", "Flat"); err == nil {
		t.Fatal("loadPreset accepted an unknown source")
	}
}
