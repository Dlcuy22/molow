package lyrics

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestSidecarNextToTrack(t *testing.T) {
	dir := t.TempDir()
	media := write(t, dir, "01. Song.flac", "")
	write(t, dir, "01. Song.lrc", "[00:12.50]あのね\n[00:20.00]だって\n")

	doc, err := Load(media)
	if err != nil {
		t.Fatal(err)
	}
	if doc == nil || len(doc.Lines) != 2 || !doc.Synced {
		t.Fatalf("doc: %+v", doc)
	}
	if doc.Lines[0].TimeMs != 12500 || doc.Lines[0].Text != "あのね" {
		t.Fatalf("line: %+v", doc.Lines[0])
	}
}

func TestIndexlessNameMatchesIndexedTrack(t *testing.T) {
	dir := t.TempDir()
	media := write(t, dir, "01. やっぱり雨は降るんだね.flac", "")
	write(t, dir, "やっぱり雨は降るんだね.lrc", "[00:01.00]雨\n")

	doc, err := Load(media)
	if err != nil || doc == nil {
		t.Fatalf("doc: %+v err: %v", doc, err)
	}
	if len(doc.Lines) != 1 {
		t.Fatalf("lines: %d", len(doc.Lines))
	}
}

func TestLyricsSubfolderAndTranslationFile(t *testing.T) {
	dir := t.TempDir()
	media := write(t, dir, "Song.flac", "")
	write(t, dir, "lyrics/Song.lrc", "[00:10.00]そら\n[00:20.00]かぜ\n")
	write(t, dir, "lyrics/Song.en.lrc", "[00:10.00]sky\n[00:20.00]wind\n")

	doc, err := Load(media)
	if err != nil || doc == nil {
		t.Fatalf("doc: %+v err: %v", doc, err)
	}
	if doc.Lines[0].Translation != "sky" || doc.Lines[1].Translation != "wind" {
		t.Fatalf("translations: %+v", doc.Lines)
	}
}

func TestBilingualSingleFile(t *testing.T) {
	dir := t.TempDir()
	media := write(t, dir, "Song.flac", "")
	write(t, dir, "Song.lrc", "[00:10.00]そら\n[00:10.00]sky\n[00:20.00]かぜ\n[00:20.00]wind\n")

	doc, err := Load(media)
	if err != nil || doc == nil {
		t.Fatal(err)
	}
	if len(doc.Lines) != 2 {
		t.Fatalf("lines: %+v", doc.Lines)
	}
	if doc.Lines[0].Text != "そら" || doc.Lines[0].Translation != "sky" {
		t.Fatalf("pair: %+v", doc.Lines[0])
	}
}

func TestMissingIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	media := write(t, dir, "Song.flac", "")
	doc, err := Load(media)
	if err != nil {
		t.Fatal(err)
	}
	if doc != nil {
		t.Fatalf("wanted nil, got %+v", doc)
	}
}
