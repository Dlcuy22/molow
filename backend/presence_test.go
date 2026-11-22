package backend

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/dlcuy22/molow/internal/discordrpc"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	ytm "github.com/dlcuy22/ytm-go"
)

// TestPresenceTrackRemoteCarriesBothButtons pins the remote mapping: the title
// is the details line, the artist the state line, the album is the cover
// tooltip, and the two links point at the track and the project.
func TestPresenceTrackRemoteCarriesBothButtons(t *testing.T) {
	snap := Snapshot{
		Path:     "ytm:abc123",
		Title:    "A Song",
		Artist:   "An Artist",
		Album:    "An Album",
		Duration: 200000,
	}

	got := presenceTrack(snap, "https://cover", "https://avatar")

	if got.Title != "A Song" || got.Artist != "An Artist" {
		t.Fatalf("title/artist = %q/%q, want the track's", got.Title, got.Artist)
	}
	if got.LargeImage != "https://cover" || got.SmallImage != "https://avatar" {
		t.Fatalf("images = %q/%q, want the supplied urls", got.LargeImage, got.SmallImage)
	}
	if got.LargeText != "An Album" || got.SmallText != "An Artist" {
		t.Fatalf("tooltips = %q/%q, want album/artist", got.LargeText, got.SmallText)
	}
	if got.DurationMs != 200000 {
		t.Fatalf("DurationMs = %d, want 200000", got.DurationMs)
	}
	if got.ActivityType != 2 {
		t.Fatalf("ActivityType = %d, want 2 (Listening)", got.ActivityType)
	}
	if len(got.Buttons) != 2 {
		t.Fatalf("buttons = %d, want 2", len(got.Buttons))
	}
	if got.Buttons[0].URL != "https://music.youtube.com/watch?v=abc123" {
		t.Fatalf("listen url = %q", got.Buttons[0].URL)
	}
}

// TestPresenceTrackLocalHasNoListenButton pins the local case: a file has no
// watch page, so only the project link is offered.
func TestPresenceTrackLocalHasNoListenButton(t *testing.T) {
	snap := Snapshot{Path: "/music/a.opus", Title: "Local", Artist: "Me"}

	got := presenceTrack(snap, "", "")

	if len(got.Buttons) != 1 {
		t.Fatalf("buttons = %d, want 1", len(got.Buttons))
	}
	if got.Buttons[0].URL != projectURL {
		t.Fatalf("button url = %q, want the project url", got.Buttons[0].URL)
	}
}

// TestPresenceTrackTrimsTopicSuffix pins the yt-dlp fix: an auto-generated
// "- Topic" channel name is trimmed from both the artist line and its tooltip.
func TestPresenceTrackTrimsTopicSuffix(t *testing.T) {
	snap := Snapshot{
		Path:   "/music/Doppelganger - kessoku band.opus",
		Title:  "Doppelganger",
		Artist: "kessoku band - Topic",
		Album:  "Doppelganger / Re:Re:",
	}

	got := presenceTrack(snap, "", "")

	if got.Artist != "kessoku band" {
		t.Fatalf("Artist = %q, want the suffix trimmed", got.Artist)
	}
	if got.SmallText != "kessoku band" {
		t.Fatalf("SmallText = %q, want the suffix trimmed", got.SmallText)
	}
}

// TestTrimArtistTopic pins the suffix rule across the shapes it must handle.
func TestTrimArtistTopic(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"kessoku band - Topic", "kessoku band"},
		{"kessoku band - topic", "kessoku band"},
		{"  kessoku band - Topic  ", "kessoku band"},
		{"kessoku band", "kessoku band"},
		{"Eve", "Eve"},
		// A real name that merely contains the word is left alone.
		{"Topic", "Topic"},
		{"A Topic Band", "A Topic Band"},
		// A name that is only the suffix would trim to empty, so it is kept.
		{"- Topic", "- Topic"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := trimArtistTopic(tc.in); got != tc.want {
			t.Errorf("trimArtistTopic(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAlbumForCachesTheLookup pins the album resolver: it calls the watch
// endpoint once, caches the name, and does not call again.
func TestAlbumForCachesTheLookup(t *testing.T) {
	index := newYTMIndex()
	index.put(ytmTrack{ID: "abc123", Title: "A Song", Artist: "An Artist"})

	calls := 0
	p := &ytmProvider{
		index: index,
		loadSong: func(_ context.Context, _ string) (*ytm.Song, error) {
			calls++

			return &ytm.Song{ID: "abc123", Album: &ytm.Playlist{Name: "An Album"}}, nil
		},
	}

	if got := p.albumFor(context.Background(), "ytm:abc123"); got != "An Album" {
		t.Fatalf("albumFor = %q, want An Album", got)
	}
	if got := p.albumFor(context.Background(), "ytm:abc123"); got != "An Album" {
		t.Fatalf("second albumFor = %q, want An Album", got)
	}
	if calls != 1 {
		t.Fatalf("loadSong calls = %d, want 1 (cached)", calls)
	}
}

// TestYtmFirstArtistIDSkipsBlanks pins the avatar key: the first credit with an
// id wins, so an unnamed credit before it does not blank the lookup.
func TestYtmFirstArtistIDSkipsBlanks(t *testing.T) {
	if got := ytmFirstArtistID(nil); got != "" {
		t.Fatalf("empty = %q, want \"\"", got)
	}

	artists := []ytm.Artist{{Name: "No Id"}, {ID: "UC123", Name: "Has Id"}}
	if got := ytmFirstArtistID(artists); got != "UC123" {
		t.Fatalf("first id = %q, want UC123", got)
	}
}

// TestDiscordEnabledRoundTrips pins the persistence: the choice survives a
// restart through the settings document, and a fresh install defaults to off.
func TestDiscordEnabledRoundTrips(t *testing.T) {
	isolateConfig(t)

	if loadAppSettings(quietLog, "loaded").Discord.Enabled {
		t.Fatal("default should be off")
	}

	def := defaultAppSettings()
	def.Discord.Enabled = true
	if err := saveAppSettings(quietLog, def); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !loadAppSettings(quietLog, "reloaded").Discord.Enabled {
		t.Fatal("enabled state did not persist")
	}

	def.Discord.Enabled = false
	if err := saveAppSettings(quietLog, def); err != nil {
		t.Fatalf("save off: %v", err)
	}
	if loadAppSettings(quietLog, "reloaded").Discord.Enabled {
		t.Fatal("disabled state did not persist")
	}
}

type fakePresenceManager struct {
	mu      sync.Mutex
	updates []discordrpc.TrackInfo
	clears  int
}

func (f *fakePresenceManager) Start()          {}
func (f *fakePresenceManager) Stop()           {}
func (f *fakePresenceManager) Connected() bool { return true }
func (f *fakePresenceManager) Clear() {
	f.mu.Lock()
	f.clears++
	f.mu.Unlock()
}
func (f *fakePresenceManager) Update(info discordrpc.TrackInfo) {
	f.mu.Lock()
	f.updates = append(f.updates, info)
	f.mu.Unlock()
}
func (f *fakePresenceManager) snapshot() []discordrpc.TrackInfo {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]discordrpc.TrackInfo(nil), f.updates...)
}

// TestSyncPresenceStreamedUsesCachedLinksWithoutPlaceholder pins the hot path:
// when a streamed track's cover URL and artist avatar are already cached, the
// very first presence update carries both links and never flashes default assets.
func TestSyncPresenceStreamedUsesCachedLinksWithoutPlaceholder(t *testing.T) {
	index := newYTMIndex()
	index.put(ytmTrack{
		ID:           "vid1",
		Title:        "Mesmerizer",
		Artist:       "32ki",
		Album:        "Mesmerizer",
		ThumbnailURL: "https://lh3.googleusercontent.com/cover",
		ArtistID:     "UCartist1",
	})
	prov := &ytmProvider{
		index:   index,
		artists: newYTMArtistIndex(),
	}
	prov.artists.put("UCartist1", "https://lh3.googleusercontent.com/avatar")

	mgr := &fakePresenceManager{}
	svc := &PlayerService{
		ctx:     context.Background(),
		ytm:     index,
		ytmProv: prov,
		presence: presenceState{
			enabled: true,
			manager: mgr,
		},
	}

	svc.syncPresence(Snapshot{
		State:  "playing",
		Path:   "ytm:vid1",
		Title:  "Mesmerizer",
		Artist: "32ki",
		Album:  "Mesmerizer",
	})
	svc.presence.wg.Wait()

	updates := mgr.snapshot()
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1 (%+v)", len(updates), updates)
	}
	if updates[0].LargeImage != "https://lh3.googleusercontent.com/cover" || updates[0].SmallImage != "https://lh3.googleusercontent.com/avatar" {
		t.Fatalf("assets = %q / %q, want cached direct links", updates[0].LargeImage, updates[0].SmallImage)
	}
}

// TestSyncPresenceStreamedHydratesFromSQLiteAfterRestart verifies a catalogued
// YTM track played after restart restores its persisted thumbnail_url and
// artist_id without calling loadSong or sending default assets.
func TestSyncPresenceStreamedHydratesFromSQLiteAfterRestart(t *testing.T) {
	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AddLibrary(library.Library{
		ID: "local:ytm", Kind: library.KindLocal, Name: "Imported",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertRemoteTrack("local:ytm", library.KindYTM, sqlite.TrackRow{
		NativeID:     "vid2",
		Title:        "Mesmerizer",
		Artist:       "32ki",
		Album:        "Mesmerizer",
		ThumbnailURL: "https://lh3.googleusercontent.com/persisted",
		ArtistID:     "UCartist2",
	}); err != nil {
		t.Fatal(err)
	}

	loadSongCalls := 0
	index := newYTMIndex()
	prov := &ytmProvider{
		index:   index,
		artists: newYTMArtistIndex(),
		loadSong: func(context.Context, string) (*ytm.Song, error) {
			loadSongCalls++
			return nil, errors.New("unexpected loadSong")
		},
	}
	prov.artists.put("UCartist2", "https://lh3.googleusercontent.com/avatar2")

	mgr := &fakePresenceManager{}
	svc := &PlayerService{
		ctx:      context.Background(),
		ytm:      index,
		ytmProv:  prov,
		libStore: store,
		presence: presenceState{
			enabled: true,
			manager: mgr,
		},
	}
	prov.catalogTrack = svc.catalogYTMTrack

	svc.syncPresence(Snapshot{
		State:  "playing",
		Path:   "ytm:vid2",
		Title:  "Mesmerizer",
		Artist: "32ki",
		Album:  "Mesmerizer",
	})
	svc.presence.wg.Wait()

	if loadSongCalls != 0 {
		t.Fatalf("loadSong called %d times, want 0", loadSongCalls)
	}
	updates := mgr.snapshot()
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1 (%+v)", len(updates), updates)
	}
	if updates[0].LargeImage != "https://lh3.googleusercontent.com/persisted" || updates[0].SmallImage != "https://lh3.googleusercontent.com/avatar2" {
		t.Fatalf("assets = %q / %q", updates[0].LargeImage, updates[0].SmallImage)
	}
}

// TestSyncPresenceStreamedPreMigrationHydratesWithoutPlaceholder verifies a
// pre-migration catalogued YTM track (missing thumbnail_url/artist_id) skips
// the empty placeholder, hydrates all fields in one loadSong call, backfills
// SQLite, and publishes real artwork.
func TestSyncPresenceStreamedPreMigrationHydratesWithoutPlaceholder(t *testing.T) {
	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AddLibrary(library.Library{
		ID: "local:ytm", Kind: library.KindLocal, Name: "Imported",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertRemoteTrack("local:ytm", library.KindYTM, sqlite.TrackRow{
		NativeID: "vid3",
		Title:    "Mesmerizer",
		Artist:   "32ki",
	}); err != nil {
		t.Fatal(err)
	}

	loadSongCalls := 0
	index := newYTMIndex()
	prov := &ytmProvider{
		index:   index,
		artists: newYTMArtistIndex(),
		loadSong: func(context.Context, string) (*ytm.Song, error) {
			loadSongCalls++
			return &ytm.Song{
				ID:        "vid3",
				Thumbnail: &ytm.ThumbnailProvider{UrlA: "https://lh3.googleusercontent.com/hydrated"},
				Artists:   []ytm.Artist{{ID: "UCartist3", Name: "32ki"}},
				Album:     &ytm.Playlist{Name: "Mesmerizer EP"},
			}, nil
		},
		loadArtist: func(context.Context, string) (*ytm.Artist, error) {
			return &ytm.Artist{
				ID:        "UCartist3",
				Thumbnail: &ytm.ThumbnailProvider{UrlA: "https://lh3.googleusercontent.com/avatar3"},
			}, nil
		},
	}

	mgr := &fakePresenceManager{}
	svc := &PlayerService{
		ctx:      context.Background(),
		ytm:      index,
		ytmProv:  prov,
		libStore: store,
		presence: presenceState{
			enabled: true,
			manager: mgr,
		},
	}
	prov.catalogTrack = svc.catalogYTMTrack
	prov.saveRemoteMeta = svc.backfillYTMTrackMeta

	svc.syncPresence(Snapshot{
		State:  "playing",
		Path:   "ytm:vid3",
		Title:  "Mesmerizer",
		Artist: "32ki",
	})
	svc.presence.wg.Wait()

	if loadSongCalls != 1 {
		t.Fatalf("loadSong called %d times, want 1", loadSongCalls)
	}
	updates := mgr.snapshot()
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1 (no placeholder send): %+v", len(updates), updates)
	}
	u := updates[0]
	if u.LargeImage != "https://lh3.googleusercontent.com/hydrated" || u.SmallImage != "https://lh3.googleusercontent.com/avatar3" || u.LargeText != "Mesmerizer EP" {
		t.Fatalf("update = %+v", u)
	}

	meta, ok, err := store.TrackRemoteMeta("ytm:vid3")
	if err != nil || !ok || meta.ThumbnailURL != "https://lh3.googleusercontent.com/hydrated" || meta.ArtistID != "UCartist3" {
		t.Fatalf("SQLite backfill = (%+v, %v, %v)", meta, ok, err)
	}
}

// TestSyncPresenceStreamedFallbackStillAvoidsDefaultLargeAsset verifies that
// even if loadSong fails offline, a streamed track falls back to YouTube's
// deterministic thumbnail URL rather than the default application icon.
func TestSyncPresenceStreamedFallbackStillAvoidsDefaultLargeAsset(t *testing.T) {
	index := newYTMIndex()
	prov := &ytmProvider{
		index:   index,
		artists: newYTMArtistIndex(),
		loadSong: func(context.Context, string) (*ytm.Song, error) {
			return nil, errors.New("offline")
		},
	}

	mgr := &fakePresenceManager{}
	svc := &PlayerService{
		ctx:     context.Background(),
		ytm:     index,
		ytmProv: prov,
		presence: presenceState{
			enabled: true,
			manager: mgr,
		},
	}

	svc.syncPresence(Snapshot{
		State:  "playing",
		Path:   "ytm:INnieyJ_QPE",
		Title:  "Mesmerizer",
		Artist: "32ki",
	})
	svc.presence.wg.Wait()

	updates := mgr.snapshot()
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1 (%+v)", len(updates), updates)
	}
	want := "https://i.ytimg.com/vi/INnieyJ_QPE/hqdefault.jpg"
	if updates[0].LargeImage != want {
		t.Fatalf("LargeImage = %q, want %q", updates[0].LargeImage, want)
	}
}
