package backend

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	ytm "github.com/dlcuy22/ytm-go"

	"github.com/dlcuy22/molow/internal/lyrics"
)

// The prefetch unit tests: the disk-side decisions run without a network, so
// they check the target name, the on-disk short circuit and the reference
// cache read. The fetch run itself is covered by the internal/lyrics chain
// tests.

func TestLyricsTargetFollowsTheSaveLocation(t *testing.T) {
	root := isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	media := filepath.Join(root, "Album", "01. Song.flac")
	meta := lyricsTrackMeta("Song (Official MV)", "Artist", 100)

	// A local track's sidecar takes the track file's own stem, not the
	// fetched metadata title, so the loader's exact-stem tier always finds it.
	if got := svc.lyricsTarget(media, "", meta); got != filepath.Join(root, "Album", "lyrics", "01. Song.lrc") {
		t.Fatalf("target = %q", got)
	}
	svc.appSettings.Playback.Lyrics.SaveLocation = "beside"
	if got := svc.lyricsTarget(media, "", meta); got != filepath.Join(root, "Album", "01. Song.lrc") {
		t.Fatalf("beside target = %q", got)
	}

	// A reference always lands as <videoId>.lrc in the default cache folder.
	want := filepath.Join(root, "molo", "lyrics", "video-1.lrc")
	if got := svc.lyricsTarget(media, "video-1", meta); got != want {
		t.Fatalf("reference target = %q, want %q", got, want)
	}
}

func TestLyricsTargetRoundTripsForAnUnlabeledRip(t *testing.T) {
	root := isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	// The tags name the song but the file does not; the target must still
	// land where the loader's exact tier looks.
	media := filepath.Join(root, "Album", "Track01.mp3")
	meta := lyricsTrackMeta("Billie Jean", "Michael Jackson", 294)

	target := svc.lyricsTarget(media, "", meta)
	if target != filepath.Join(root, "Album", "lyrics", "Track01.lrc") {
		t.Fatalf("target = %q", target)
	}
	if err := lyrics.Write(target, "[00:29.26]She was more like a beauty queen\n"); err != nil {
		t.Fatal(err)
	}

	doc, err := lyrics.Load(media)
	if err != nil || doc == nil {
		t.Fatalf("doc %+v err %v", doc, err)
	}
	if len(doc.Lines) != 1 {
		t.Fatalf("lines: %+v", doc.Lines)
	}
}

func TestSyncLyricsShortCircuitsOnDisk(t *testing.T) {
	root := isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	media := filepath.Join(root, "Album", "01. Song.flac")
	if err := lyrics.Write(filepath.Join(root, "Album", "Song.lrc"), "[00:01.00]そら\n"); err != nil {
		t.Fatal(err)
	}

	svc.syncLyrics(Snapshot{
		Path:     media,
		Title:    "Song",
		Artist:   "Artist",
		Album:    "Album",
		Duration: 100000,
	})

	svc.lyrics.mu.Lock()
	job, done, missing := svc.lyrics.jobPath, svc.lyrics.donePath, len(svc.lyrics.missing)
	svc.lyrics.mu.Unlock()

	if job != "" {
		t.Fatalf("job started for a track whose sidecar exists: %q", job)
	}
	if done != media {
		t.Fatalf("done = %q, want %q", done, media)
	}
	if missing != 0 {
		t.Fatalf("missing = %d, want 0", missing)
	}
}

// TestSyncLyricsRepeatedTicksCostOneDiskLook pins the pump's idempotence: a
// track re-reported on every tick gets one disk look. Without it the trail
// shows the "already on disk" line once per tick.
func TestSyncLyricsRepeatedTicksCostOneDiskLook(t *testing.T) {
	root := isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	media := filepath.Join(root, "Album", "Song.flac")
	if err := lyrics.Write(filepath.Join(root, "Album", "Song.lrc"), "[00:01.00]そら\n"); err != nil {
		t.Fatal(err)
	}

	trail := &strings.Builder{}
	prev := slog.Default()
	svc.log = slog.New(slog.NewTextHandler(trail, &slog.HandlerOptions{Level: slog.LevelDebug}))
	defer slog.SetDefault(prev)

	snap := Snapshot{Path: media, Title: "Song", Duration: 100000}
	for i := 0; i < 3; i++ {
		svc.syncLyrics(snap)
	}
	if got := strings.Count(trail.String(), "sidecar already on disk"); got != 1 {
		t.Fatalf("disk look logged %d times for one track, want 1:\n%s", got, trail.String())
	}

	svc.lyrics.mu.Lock()
	job := svc.lyrics.jobPath
	svc.lyrics.mu.Unlock()
	if job != "" {
		t.Fatalf("job started for a track whose sidecar exists: %q", job)
	}
}

// TestSyncLyricsDiskHitLogsTheSidecar pins the trace field: the "already on
// disk" line names the lyric file that answered, not the track that triggered
// the check.
func TestSyncLyricsDiskHitLogsTheSidecar(t *testing.T) {
	root := isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	media := filepath.Join(root, "Album", "Michael Jackson - Billie Jean.opus")
	sidecar := filepath.Join(root, "Album", "lyrics", "Michael Jackson - Billie Jean.lrc")
	if err := lyrics.Write(sidecar, "[00:01.00]そら\n"); err != nil {
		t.Fatal(err)
	}

	trail := &strings.Builder{}
	svc.log = slog.New(slog.NewTextHandler(trail, &slog.HandlerOptions{Level: slog.LevelDebug}))

	svc.syncLyrics(Snapshot{Path: media, Title: "Billie Jean", Duration: 100000})

	// slog quotes the value and escapes the separators, so on Windows the
	// rendered line carries doubled backslashes; compare the quoted form.
	if got := trail.String(); !strings.Contains(got, strconv.Quote(sidecar)) {
		t.Fatalf("log line does not name %q:\n%s", sidecar, got)
	}
	if strings.Contains(trail.String(), strconv.Quote(media)) {
		t.Fatalf("log line names the track, not the sidecar:\n%s", trail.String())
	}
}

// TestSyncLyricsDiskHitClearsTheStaleSheet pins the done/donePath pair's
// atomicity: a disk short-circuit concludes a track without a sheet of its
// own, so the previous track's sheet must not survive in done. A join after
// the short-circuit used to serve it as the new track's lyrics.
func TestSyncLyricsDiskHitClearsTheStaleSheet(t *testing.T) {
	root := isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()
	svc.ytmProv = lyricProvider(
		func(context.Context, string) (*ytm.Song, error) {
			return &ytm.Song{ID: "abc123", LyricsBrowseID: "MPLYt_x"}, nil
		},
		func(context.Context, string) (*ytm.Lyrics, error) {
			return &ytm.Lyrics{Lines: []ytm.LyricLine{{Text: "prev track's sheet"}}}, nil
		},
	)
	for name := range svc.appSettings.Playback.Lyrics.Providers {
		svc.appSettings.Playback.Lyrics.Providers[name] = false
	}

	// A reference's chain concludes; done holds the catalogue sheet.
	svc.syncLyrics(Snapshot{Path: "ytm:abc123", Duration: 100000})
	svc.lyrics.mu.Lock()
	concluded := svc.lyrics.jobDone
	svc.lyrics.mu.Unlock()
	<-concluded

	media := filepath.Join(root, "Album", "Song.flac")
	if err := lyrics.Write(filepath.Join(root, "Album", "lyrics", "Song.lrc"), "[00:01.00]そら\n"); err != nil {
		t.Fatal(err)
	}

	// The next track answers from disk: donePath moves, done must not leak.
	svc.syncLyrics(Snapshot{Path: media, Title: "Song", Duration: 100000})
	if doc, ok := svc.joinLyrics(media); doc != nil || ok {
		t.Fatalf("join = %+v %v, want nil false after a disk short-circuit", doc, ok)
	}

	// The track's own sheet still reads through the loader.
	doc, err := svc.Lyrics(media)
	if err != nil || doc == nil {
		t.Fatalf("Lyrics = %+v, %v; want the disk sidecar", doc, err)
	}
	if doc.Lines[0].Text != "そら" {
		t.Fatalf("line = %+v", doc.Lines[0])
	}
}

func TestSyncLyricsSkipsTheUntaggedTrack(t *testing.T) {
	isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	// A local track queued but not yet tagged has nothing to search for; the
	// tags arrive with the same pump.
	svc.syncLyrics(Snapshot{Path: "/somewhere/track.flac", Duration: 100000})

	svc.lyrics.mu.Lock()
	job := svc.lyrics.jobPath
	svc.lyrics.mu.Unlock()
	if job != "" {
		t.Fatalf("job started for an untagged track: %q", job)
	}
}

func TestReadLyricsFromTheCacheFolder(t *testing.T) {
	root := isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	if err := lyrics.Write(filepath.Join(root, "molo", "lyrics", "video-1.lrc"), "[00:01.00]そら\n[00:02.00]かぜ\n"); err != nil {
		t.Fatal(err)
	}

	doc, err := svc.readLyrics("ytm:video-1")
	if err != nil || doc == nil {
		t.Fatalf("doc %+v err %v", doc, err)
	}
	if doc.Source != filepath.Join(root, "molo", "lyrics", "video-1.lrc") {
		t.Fatalf("source = %q", doc.Source)
	}
	if doc.Lines[0].Text != "そら" {
		t.Fatalf("lines: %+v", doc.Lines)
	}
	if _, err := os.Stat(filepath.Join(root, "molo", "lyrics")); err != nil {
		t.Fatalf("default cache folder not created: %v", err)
	}
}

func TestLyricsJoinMissWithoutAJob(t *testing.T) {
	isolateConfig(t)

	svc := newPlayerService()
	svc.player = newFakePlayer()

	if doc, ok := svc.joinLyrics("ytm:none"); doc != nil || ok {
		t.Fatalf("join = %v %v, want nil false with no job running", doc, ok)
	}
}

func lyricsTrackMeta(title, artist string, sec int) lyrics.TrackMeta {
	return lyrics.TrackMeta{Title: title, Artist: artist, DurationSec: sec}
}
