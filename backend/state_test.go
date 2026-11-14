package backend

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionStateDefaults pins a fresh install: full volume, the rail at the
// shipped width.
func TestSessionStateDefaults(t *testing.T) {
	isolateConfig(t)

	got := loadSessionState(quietLog)
	if got.Version != stateVersion {
		t.Fatalf("version = %d, want %d", got.Version, stateVersion)
	}
	if got.Volume != 1 {
		t.Fatalf("volume = %v, want 1", got.Volume)
	}
	if got.SidebarWidth != defaultSidebarWidth {
		t.Fatalf("sidebar width = %d, want %d", got.SidebarWidth, defaultSidebarWidth)
	}
}

// TestSessionStateRoundTrips proves the hot values survive a restart, which is
// the whole point of the document.
func TestSessionStateRoundTrips(t *testing.T) {
	isolateConfig(t)

	want := SessionState{Volume: 0.42, SidebarWidth: 240}
	if err := saveSessionState(want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := loadSessionState(quietLog)
	if got.Volume != want.Volume {
		t.Fatalf("loaded volume = %v, want %v", got.Volume, want.Volume)
	}
	if got.SidebarWidth != want.SidebarWidth {
		t.Fatalf("loaded sidebar width = %d, want %d", got.SidebarWidth, want.SidebarWidth)
	}
}

// TestSessionStateNormalizes pins the bounds: a volume outside [0, 1] or not a
// number lands at zero, and the width clamps to the resize handle's range.
func TestSessionStateNormalizes(t *testing.T) {
	isolateConfig(t)

	p, err := statePath()
	if err != nil {
		t.Fatalf("statePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		body       string
		wantVolume float64
		wantWidth  int
	}{
		{"in range", `{"volume":0.3,"sidebarWidth":160}`, 0.3, 160},
		{"over", `{"volume":1.5,"sidebarWidth":500}`, 1, maxSidebarWidth},
		{"under", `{"volume":-1,"sidebarWidth":10}`, 0, minSidebarWidth},
		{"malformed", `{"volume":"loud","sidebarWidth":160}`, 1, defaultSidebarWidth},
	}
	for _, c := range cases {
		if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		got := loadSessionState(quietLog)
		if got.Volume != c.wantVolume {
			t.Fatalf("%s: volume = %v, want %v", c.name, got.Volume, c.wantVolume)
		}
		if got.SidebarWidth != c.wantWidth {
			t.Fatalf("%s: sidebar width = %d, want %d", c.name, got.SidebarWidth, c.wantWidth)
		}
	}
}

// TestSessionStateRejectsNaN is its own case because a NaN never compares
// equal, so the range check alone would let one through.
func TestSessionStateRejectsNaN(t *testing.T) {
	isolateConfig(t)

	p, err := statePath()
	if err != nil {
		t.Fatalf("statePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// JSON cannot carry NaN, but a hand-edit or a buggy writer can name it
	// through a string the decoder refuses; the decoder path is what the
	// range test covers. This pins the normalize guard directly.
	got := normalizeSessionState(SessionState{Volume: math.NaN()})
	if got.Volume != 0 {
		t.Fatalf("NaN volume = %v, want 0", got.Volume)
	}
	got = normalizeSessionState(SessionState{Volume: math.Inf(1)})
	if got.Volume != 0 {
		t.Fatalf("Inf volume = %v, want 0", got.Volume)
	}
}

// TestSessionStateDebouncedSave pins the write path: a volume change
// schedules the document, and the write lands once, half a second after the
// last change, not per step.
func TestSessionStateDebouncedSave(t *testing.T) {
	isolateConfig(t)

	svc := &PlayerService{closing: make(chan struct{})}
	svc.recordVolume(0.42)
	svc.recordVolume(0.43)

	p, err := statePath()
	if err != nil {
		t.Fatalf("statePath: %v", err)
	}

	// Two changes, one write: the file does not exist before the debounce
	// window ends and holds the last value after it.
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("the document was written before the debounce ended")
	}
	time.Sleep(700 * time.Millisecond)

	got := loadSessionState(quietLog)
	if got.Volume != 0.43 {
		t.Fatalf("debounced volume = %v, want 0.43", got.Volume)
	}

	// The timer fired; a shutdown flush must not reschedule it.
	svc.stateMu.Lock()
	fired := svc.stateTimer == nil
	svc.stateMu.Unlock()
	if !fired {
		t.Fatal("the debounce timer survived its own write")
	}
}

// TestSessionStateIgnoresSilence pins the mute rule: a zero is mute or a drag
// to silence, so the stored level stays the last audible one.
func TestSessionStateIgnoresSilence(t *testing.T) {
	isolateConfig(t)

	svc := &PlayerService{closing: make(chan struct{})}
	svc.state = defaultSessionState()
	svc.recordVolume(0.6)
	svc.recordVolume(0)
	svc.recordVolume(0)

	svc.stateMu.Lock()
	got := svc.state.Volume
	svc.stateMu.Unlock()
	if got != 0.6 {
		t.Fatalf("stored volume = %v, want the last audible 0.6", got)
	}
}
