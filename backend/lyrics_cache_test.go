package backend

import (
	"os"
	"path/filepath"
	"testing"
)

// TestClearLyricsCacheRemovesOnlyTopLevelSidecars pins the safe scope: the
// folder is user-chosen, so a nested sheet and an unrelated file must survive.
func TestClearLyricsCacheRemovesOnlyTopLevelSidecars(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "keep")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	sidecar := filepath.Join(dir, "abc123.lrc")
	if err := os.WriteFile(sidecar, []byte("one line"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a sheet"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "nested.lrc"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := &PlayerService{appSettings: defaultAppSettings()}
	svc.appSettings.Playback.Lyrics.Folder = dir

	got, err := svc.ClearLyricsCache()
	if err != nil {
		t.Fatalf("ClearLyricsCache: %v", err)
	}
	if got.Files != 1 || got.Bytes != int64(len("one line")) {
		t.Fatalf("removed = %+v, want one 8 byte sheet", got)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("sidecar still on disk: %v", err)
	}
	for _, keep := range []string{filepath.Join(dir, "notes.txt"), filepath.Join(nested, "nested.lrc")} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s should survive: %v", keep, err)
		}
	}
}
