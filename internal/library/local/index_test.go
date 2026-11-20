package local

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseTrackIndex(t *testing.T) {
	tests := []struct {
		file  string
		disc  int
		track int
		rest  string
		ok    bool
	}{
		{"01. それでも雨は降るんだね.flac", 0, 1, "それでも雨は降るんだね", true},
		{"1. Song.flac", 0, 1, "Song", true},
		{"01.Song.flac", 0, 1, "Song", true},
		{"1 Song.flac", 0, 1, "Song", true},
		{"01 Song.flac", 0, 1, "Song", true},
		{"10 Song.flac", 0, 10, "Song", true},
		{"01 - Song.flac", 0, 1, "Song", true},
		{"01-Song.flac", 0, 1, "Song", true},
		{"01 – Song.flac", 0, 1, "Song", true},
		{"01_Song.flac", 0, 1, "Song", true},
		{"(01) Song.flac", 0, 1, "Song", true},
		{"[02] Song.flac", 0, 2, "Song", true},
		{"[02]Song.flac", 0, 2, "Song", true},
		{"03) Song.flac", 0, 3, "Song", true},
		{"#04 Song.flac", 0, 4, "Song", true},
		{"Track 05 - Song.flac", 0, 5, "Song", true},
		{"No. 6 Song.flac", 0, 6, "Song", true},
		{"01.flac", 0, 1, "", true},
		{"1.flac", 0, 1, "", true},
		{"1-01. Song.flac", 1, 1, "Song", true},
		{"2-03 Song.flac", 2, 3, "Song", true},
		{"CD2-04 - Song.flac", 2, 4, "Song", true},
		{"Disc 2 - 05 Song.flac", 2, 5, "Song", true},
		{"D1T06 Song.flac", 1, 6, "Song", true},
		{"01. 1984.flac", 0, 1, "1984", true},
		{"1984.flac", 0, 0, "1984", false},
		{"2020 - Song.flac", 0, 0, "2020 - Song", false},
		{"3AM.flac", 0, 0, "3AM", false},
		{"24K Magic.flac", 0, 0, "24K Magic", false},
		{"3.14 Pi.flac", 0, 0, "3.14 Pi", false},
		{"00. Intro.flac", 0, 0, "00. Intro", false},
		{"Plain Title.flac", 0, 0, "Plain Title", false},
	}

	for _, tc := range tests {
		disc, track, rest, ok := parseTrackIndex(filepath.Join("/album", tc.file))
		if ok != tc.ok || disc != tc.disc || track != tc.track || rest != tc.rest {
			t.Errorf("parseTrackIndex(%q) = (%d, %d, %q, %v), want (%d, %d, %q, %v)",
				tc.file, disc, track, rest, ok, tc.disc, tc.track, tc.rest, tc.ok)
		}
	}
}

func TestTitleFromPath(t *testing.T) {
	if got := titleFromPath("/album/01. Song.flac"); got != "Song" {
		t.Fatalf("title = %q, want Song", got)
	}
	if got := titleFromPath("/album/01.flac"); got != "01" {
		t.Fatalf("bare numeric title = %q, want 01", got)
	}
	if got := titleFromPath("/album/1984 - Song.flac"); got != "1984 - Song" {
		t.Fatalf("year title = %q, want 1984 - Song", got)
	}
}

func TestSortPathsByIndex(t *testing.T) {
	paths := []string{
		filepath.Join("/root/a", "10 Tenth.flac"),
		filepath.Join("/root/a", "2 Second.flac"),
		filepath.Join("/root/a", "Bonus.flac"),
		filepath.Join("/root/a", "1 First.flac"),
		filepath.Join("/root/b", "2-01 DiscTwo.flac"),
		filepath.Join("/root/b", "1-10 DiscOneTen.flac"),
		filepath.Join("/root/b", "1-02 DiscOneTwo.flac"),
	}
	sortPathsByIndex(paths)

	want := []string{
		filepath.Join("/root/a", "1 First.flac"),
		filepath.Join("/root/a", "2 Second.flac"),
		filepath.Join("/root/a", "10 Tenth.flac"),
		filepath.Join("/root/a", "Bonus.flac"),
		filepath.Join("/root/b", "1-02 DiscOneTwo.flac"),
		filepath.Join("/root/b", "1-10 DiscOneTen.flac"),
		filepath.Join("/root/b", "2-01 DiscTwo.flac"),
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("sorted =\n%v\nwant:\n%v", paths, want)
	}
}

func TestIndexAwareScanOrdersAlbumTracksAndRefs(t *testing.T) {
	src, store := newTestLocal(t)
	ext := testExt(t)

	root := t.TempDir()
	albumDir := filepath.Join(root, "My Album [FLAC]")
	p1 := filepath.Join(albumDir, "1 First"+ext)
	p2 := filepath.Join(albumDir, "02. Second"+ext)
	p3 := filepath.Join(albumDir, "03 Third"+ext)
	p10 := filepath.Join(albumDir, "10 Tenth"+ext)
	writeFiles(t, p10, p2, p1, p3)

	if _, err := store.AddRoot("local:test", root); err != nil {
		t.Fatalf("add root: %v", err)
	}
	res, err := src.ImportScan(context.Background(), root)
	if err != nil {
		t.Fatalf("import scan: %v", err)
	}

	wantRefs := []string{p1, p2, p3, p10}
	if !slices.Equal(res.Refs, wantRefs) {
		t.Fatalf("refs = %v, want %v", res.Refs, wantRefs)
	}

	tracks, err := store.AlbumTracks("local:test", "My Album", "")
	if err != nil {
		t.Fatalf("album tracks: %v", err)
	}
	wantNos := []int{1, 2, 3, 10}
	if len(tracks) != len(wantNos) {
		t.Fatalf("got %d tracks, want %d", len(tracks), len(wantNos))
	}
	for i, want := range wantNos {
		if tracks[i].TrackNo != want {
			t.Fatalf("track[%d].TrackNo = %d, want %d (all: %+v)", i, tracks[i].TrackNo, want, tracks)
		}
	}
}
