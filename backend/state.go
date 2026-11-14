package backend

import (
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
)

// stateVersion is the schema version of the state document, the same role
// settings.json's version tag plays.
const stateVersion = 1

// SessionState is the state document: values the machine writes while the user
// works, unlike the settings document, which only changes when the user edits
// a preference. Volume is the master gain the engine last held, stored as the
// last audible level so a restart after a muted quit lands on sound rather
// than silence. SidebarWidth is the navigation rail's width in pixels. Both
// change often, so the document is written on a debounce, not on every step.
type SessionState struct {
	Version      int     `json:"version"`
	Volume       float64 `json:"volume"`
	SidebarWidth int     `json:"sidebarWidth"`
}

// statePath resolves where the state document lives: beside the settings
// document in the config folder.
func statePath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(base, "molow", "state.json"), nil
}

// defaultSessionState is a fresh install's state: full volume, the rail at the
// shipped width.
func defaultSessionState() SessionState {
	return SessionState{
		Version:      stateVersion,
		Volume:       1,
		SidebarWidth: defaultSidebarWidth,
	}
}

// loadSessionState reads the persisted state, falling back to the defaults for
// a missing, unreadable, or malformed file. It never returns an error: the
// state document is a convenience, and a bad one must not stop the app.
func loadSessionState(log *slog.Logger) SessionState {
	def := defaultSessionState()

	p, err := statePath()
	if err != nil {
		return def
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return def
	}

	var s SessionState
	if err := json.Unmarshal(raw, &s); err != nil {
		log.Warn("state: unreadable, using defaults", "path", p, "error", err)

		return def
	}
	log.Info("state: loaded", "path", p)

	return normalizeSessionState(s)
}

// normalizeSessionState bounds both values to what the controls can produce,
// so a hand-edit or a corrupted write cannot wedge the UI. A volume outside
// [0, 1] clamps to the nearest bound; one that is not a number at all lands at
// zero, and a zero restores as silence.
func normalizeSessionState(s SessionState) SessionState {
	s.Version = stateVersion

	if math.IsNaN(s.Volume) || math.IsInf(s.Volume, 0) {
		s.Volume = 0
	} else {
		s.Volume = clamp01(s.Volume)
	}
	s.SidebarWidth = clampSidebarWidth(s.SidebarWidth)

	return s
}

// saveSessionState writes the state document with the same temp-sync-rename
// dance the settings document uses, so a crash mid-write cannot publish a
// torn file.
func saveSessionState(s SessionState) error {
	s.Version = stateVersion

	p, err := statePath()
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

	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
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
	// A state file is readable by the user, not secret, and CreateTemp makes
	// 0600, so the mode is set explicitly.
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)

		return err
	}

	return os.Rename(name, p)
}

// Sidebar width bounds are the resize handle's own range in the frontend, so a
// width the drag cannot produce cannot persist either.
const (
	minSidebarWidth     = 56
	maxSidebarWidth     = 280
	defaultSidebarWidth = 200
)

// clampSidebarWidth bounds the persisted sidebar width to the handle's range.
func clampSidebarWidth(px int) int {
	if px < minSidebarWidth {
		return minSidebarWidth
	}
	if px > maxSidebarWidth {
		return maxSidebarWidth
	}

	return px
}
