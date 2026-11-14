package backend

import (
	"testing"

	"github.com/dlcuy22/molo"
)

// meteringPlayer counts the EffectMeters reads a pump makes, so a test can
// tell a gated tick from one that reached the engine.
type meteringPlayer struct {
	*fakePlayer
	reads int
}

func (m *meteringPlayer) EffectMeters() []molo.EffectMeters {
	m.reads++

	return nil
}

// TestEffectMetersGateStopsTheEngineRead pins the effects-window gate: with
// nothing drawing the meters the pump does not reach the engine at all, and
// with the window present it does.
func TestEffectMetersGateStopsTheEngineRead(t *testing.T) {
	svc := spectrumService(t)
	p := &meteringPlayer{fakePlayer: newFakePlayer()}
	svc.player = p

	svc.effectWindowPresent = func() bool { return false }
	svc.publishEffectMeters()
	if p.reads != 0 {
		t.Fatalf("closed effects window read the meters %d times, want 0", p.reads)
	}

	svc.effectWindowPresent = func() bool { return true }
	svc.publishEffectMeters()
	if p.reads == 0 {
		t.Fatal("open effects window pump never read the meters")
	}
}

// TestEffectMetersDefaultKeepsFeeding pins the production default: with no
// application (tests, server mode) the gate stays open, so behaviour is
// unchanged where there is no window system.
func TestEffectMetersDefaultKeepsFeeding(t *testing.T) {
	svc := spectrumService(t)

	if !svc.effectWindowPresent() {
		t.Fatal("no application must keep the meter pump feeding")
	}
}
