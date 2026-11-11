package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlcuy22/molow/internal/lyrics"
)

// The instrumental marks exist only in the document the pane draws. The
// sidecar on disk keeps the provider's text, so a local sheet is never
// rewritten for a display decision.
func TestLyricsMarksStayOutOfTheSidecar(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	svc := newPlayerService()
	fake := newFakePlayer()
	svc.player = fake

	dir := filepath.Join(root, "Album")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "song.mp3")
	sidecar := filepath.Join(dir, "song.lrc")
	const sheet = "[00:00.00]one\n" +
		"<o:0.000:1.000|n:1.000:2.000>\n" +
		"[00:08.00]two\n" +
		"<t:8.000:9.000>\n"
	if err := os.WriteFile(sidecar, []byte(sheet), 0o644); err != nil {
		t.Fatal(err)
	}

	fake.path = media
	fake.duration = 10 * time.Second

	doc, err := svc.Lyrics(media)
	if err != nil || doc == nil {
		t.Fatalf("Lyrics = %+v, %v", doc, err)
	}
	if len(doc.Lines) != 3 {
		t.Fatalf("lines = %d, want the two sung lines and a mark", len(doc.Lines))
	}
	if doc.Lines[1].Text != lyrics.InstrumentalMark {
		t.Fatalf("mark = %q", doc.Lines[1].Text)
	}

	after, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), lyrics.InstrumentalMark) {
		t.Fatalf("the sidecar gained a mark:\n%s", after)
	}
}
