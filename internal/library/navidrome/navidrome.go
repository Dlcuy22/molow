// Package navidrome implements library.Source over a remote Navidrome server,
// syncing the server's catalogue and playlists into the local SQLite store so
// browsing, FTS5 search, likes, and playlist editing work uniformly with local
// libraries.
package navidrome

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	nd "github.com/dlcuy22/molow/internal/navidrome"
)

const (
	syncAlbumPageSize = 500
	syncSongPageSize  = 500
	syncAlbumWorkers  = 6
	syncCoverWorkers  = 10
	coverArtSize      = 512
)

// SyncProgress reports scan/sync progress to the UI.
type SyncProgress struct {
	LibraryID string
	Done      int
	Total     int
}

// Source is one browsable Navidrome library backed by SQLite and a Subsonic client.
type Source struct {
	store  *sqlite.Store
	art    *artwork.Store
	lib    library.Library
	client *nd.Client

	mu       sync.Mutex
	status   library.Status
	message  string
	onChange func(SyncProgress)
}

// New builds a Navidrome library source over an existing SQLite row and client.
func New(store *sqlite.Store, lib library.Library, art *artwork.Store, client *nd.Client, onChange func(SyncProgress)) *Source {
	status := lib.Status
	if status == "" {
		status = library.StatusReady
	}

	return &Source{
		store:    store,
		art:      art,
		lib:      lib,
		client:   client,
		status:   status,
		onChange: onChange,
	}
}

// ParseRef extracts the owning library ID and Navidrome song ID from a playable ref.
func ParseRef(ref string) (libraryID, songID string, ok bool) {
	kind, libID, nativeID, ok := library.ParseRemoteRef(ref)
	if !ok || kind != library.KindNavidrome || nativeID == "" {
		return "", "", false
	}

	return libID, nativeID, true
}

// CanRefresh reports whether this source can refresh from its remote Navidrome server.
func (s *Source) CanRefresh() bool {
	return true
}

// SetClient updates the Subsonic client after credentials or server URL change.
func (s *Source) SetClient(client *nd.Client) {
	s.mu.Lock()
	s.client = client
	s.mu.Unlock()
}

func (s *Source) getClient() *nd.Client {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.client
}

func (s *Source) setStatus(status library.Status, message string) {
	s.mu.Lock()
	s.status = status
	s.message = message
	s.mu.Unlock()
}

// Describe reports the library with its live track count and capabilities.
func (s *Source) Describe(context.Context) (library.Library, error) {
	lib, err := s.store.Library(s.lib.ID)
	if err != nil {
		return s.lib, err
	}
	s.mu.Lock()
	status := s.status
	msg := s.message
	s.mu.Unlock()

	lib.Kind = library.KindNavidrome
	lib.Status = status
	lib.Message = msg
	lib.Capabilities = library.CapabilitiesOf(s)

	return lib, nil
}

// Feed answers one browsing tab from the synced SQLite catalogue.
func (s *Source) Feed(_ context.Context, tab, cursor string, limit int) (library.Page, error) {
	offset, _ := strconv.Atoi(cursor)
	if limit <= 0 {
		limit = 60
	}

	switch {
	case tab == "all":
		playlists, err := s.store.Playlists(s.lib.ID)
		if err != nil {
			return library.Page{}, err
		}
		albums, total, err := s.store.Albums(s.lib.ID, limit, offset)
		if err != nil {
			return library.Page{}, err
		}
		shelves := make([]library.Shelf, 0, 2)
		if len(playlists) > 0 {
			shelves = append(shelves, library.Shelf{
				ID: "playlists", Title: "Playlists", Layout: "grid", Kind: "playlists",
				Playlists: playlists, Total: len(playlists),
			})
		}
		shelves = append(shelves, library.Shelf{
			ID: "albums", Title: "Albums", Layout: "grid", Kind: "albums",
			Albums: albums, Total: total, Cursor: nextCursor(offset+len(albums), total),
		})

		return library.Page{Shelves: shelves}, nil
	case tab == "" || tab == "home":
		return s.home(limit)
	case tab == "tracks":
		items, total, err := s.store.Tracks(s.lib.ID, limit, offset)
		if err != nil {
			return library.Page{}, err
		}

		return library.Page{Shelves: []library.Shelf{{
			ID: "tracks", Title: "Songs", Layout: "list", Kind: "tracks",
			Items: items, Total: total, Cursor: nextCursor(offset+len(items), total),
		}}}, nil
	case tab == "albums":
		albums, total, err := s.store.Albums(s.lib.ID, limit, offset)
		if err != nil {
			return library.Page{}, err
		}

		return library.Page{Shelves: []library.Shelf{{
			ID: "albums", Title: "Albums", Layout: "grid", Kind: "albums",
			Albums: albums, Total: total, Cursor: nextCursor(offset+len(albums), total),
		}}}, nil
	case tab == "artists":
		artists, total, err := s.store.Artists(s.lib.ID, limit, offset)
		if err != nil {
			return library.Page{}, err
		}

		return library.Page{Shelves: []library.Shelf{{
			ID: "artists", Title: "Artists", Layout: "grid", Kind: "artists",
			Artists: artists, Total: total, Cursor: nextCursor(offset+len(artists), total),
		}}}, nil
	case tab == "playlists":
		list, err := s.store.Playlists(s.lib.ID)
		if err != nil {
			return library.Page{}, err
		}

		return library.Page{Shelves: []library.Shelf{{
			ID: "playlists", Title: "Playlists", Layout: "grid", Kind: "playlists",
			Playlists: list, Total: len(list),
		}}}, nil
	case strings.HasPrefix(tab, "playlist:"):
		id := strings.TrimPrefix(tab, "playlist:")
		total, err := s.store.PlaylistTrackCount(id)
		if err != nil {
			return library.Page{}, err
		}
		items, err := s.store.PlaylistTracks(id, limit, offset)
		if err != nil {
			return library.Page{}, err
		}

		return library.Page{Shelves: []library.Shelf{{
			ID: id, Title: "Playlist", Layout: "list", Kind: "tracks",
			Items: items, Total: total, Cursor: nextCursor(offset+len(items), total),
		}}}, nil
	}

	return library.Page{}, fmt.Errorf("library: unknown tab %q", tab)
}

func (s *Source) home(limit int) (library.Page, error) {
	if limit > 9 {
		limit = 9
	}
	tracks, err := s.store.RecentTracks(s.lib.ID, limit)
	if err != nil {
		return library.Page{}, err
	}
	if len(tracks) == 0 {
		return library.Page{}, nil
	}
	albums, _, err := s.store.Albums(s.lib.ID, 5, 0)
	if err != nil {
		return library.Page{}, err
	}
	artists, _, err := s.store.Artists(s.lib.ID, 5, 0)
	if err != nil {
		return library.Page{}, err
	}

	return library.Page{Shelves: []library.Shelf{
		{ID: "recent", Title: "Recently added", Layout: "grid", Kind: "tracks", Items: tracks},
		{ID: "albums", Title: "Albums", Layout: "grid", Kind: "albums", Albums: albums},
		{ID: "artists", Title: "Artists", Layout: "grid", Kind: "artists", Artists: artists},
	}}, nil
}

// Tracks resolves refs to catalogue rows.
func (s *Source) Tracks(_ context.Context, refs []string) ([]library.Track, error) {
	out := make([]library.Track, 0, len(refs))
	for _, ref := range refs {
		t, ok, err := s.store.TrackByRef(ref)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, t)
		}
	}

	return out, nil
}

// AlbumTracks reads one album in disc and track order.
func (s *Source) AlbumTracks(_ context.Context, album, artist string) ([]library.Track, error) {
	return s.store.AlbumTracks(s.lib.ID, album, artist)
}

// ArtistTracks reads every track by one artist in album and track order.
func (s *Source) ArtistTracks(_ context.Context, artist string) ([]library.Track, error) {
	return s.store.ArtistTracks(s.lib.ID, artist)
}

// Search queries the synced FTS5 index for matching songs.
func (s *Source) Search(_ context.Context, query string, limit int) (library.Page, error) {
	if limit <= 0 {
		limit = 40
	}
	items, err := s.store.Search(s.lib.ID, query, limit)
	if err != nil {
		return library.Page{}, err
	}

	return library.Page{Shelves: []library.Shelf{{
		ID: "search", Title: "Songs", Layout: "list", Kind: "tracks", Items: items,
	}}}, nil
}

// SetLiked stars or unstars a track on Navidrome and updates the local like table.
func (s *Source) SetLiked(ctx context.Context, ref string, liked bool) error {
	if client := s.getClient(); client != nil {
		if _, songID, ok := ParseRef(ref); ok {
			if err := client.SetStarred(ctx, songID, liked); err != nil {
				return err
			}
		}
	}

	return s.store.SetLiked(ref, liked)
}

// Playlists lists synced playlists for this Navidrome library.
func (s *Source) Playlists(context.Context) ([]library.Playlist, error) {
	return s.store.Playlists(s.lib.ID)
}

// PlaylistTracks returns one page of playlist entries.
func (s *Source) PlaylistTracks(ctx context.Context, id, cursor string, limit int) (library.Page, error) {
	offset, _ := strconv.Atoi(cursor)

	return s.Feed(ctx, "playlist:"+id, strconv.Itoa(offset), limit)
}

// CreatePlaylist creates a playlist on Navidrome and mirrors it in SQLite.
func (s *Source) CreatePlaylist(ctx context.Context, name string) (library.Playlist, error) {
	client := s.getClient()
	if client == nil {
		return library.Playlist{}, fmt.Errorf("navidrome: client not configured")
	}
	remote, err := client.CreatePlaylist(ctx, name, nil)
	if err != nil {
		return library.Playlist{}, err
	}
	title := strings.TrimSpace(remote.Name)
	if title == "" {
		title = name
	}

	return s.store.CreatePlaylistWithRemote(s.lib.ID, title, remote.ID)
}

func (s *Source) remotePlaylistID(playlistID string) (string, error) {
	lists, err := s.store.Playlists(s.lib.ID)
	if err != nil {
		return "", err
	}
	for _, pl := range lists {
		if pl.ID == playlistID {
			return pl.RemoteID, nil
		}
	}

	return "", fmt.Errorf("playlist: unknown id %q", playlistID)
}

// AddToPlaylist appends tracks to both the Navidrome playlist and SQLite.
func (s *Source) AddToPlaylist(ctx context.Context, playlistID string, refs []string) error {
	remoteID, err := s.remotePlaylistID(playlistID)
	if err != nil {
		return err
	}
	if remoteID != "" {
		if client := s.getClient(); client != nil {
			songIDs := make([]string, 0, len(refs))
			for _, ref := range refs {
				if _, songID, ok := ParseRef(ref); ok {
					songIDs = append(songIDs, songID)
				}
			}
			if len(songIDs) > 0 {
				if err := client.UpdatePlaylist(ctx, remoteID, nil, songIDs, nil); err != nil {
					return err
				}
			}
		}
	}

	return s.store.AddToPlaylist(playlistID, refs)
}

// RemoveFromPlaylist deletes tracks from both the Navidrome playlist and SQLite.
func (s *Source) RemoveFromPlaylist(ctx context.Context, playlistID string, refs []string) error {
	remoteID, err := s.remotePlaylistID(playlistID)
	if err != nil {
		return err
	}
	if remoteID != "" {
		if client := s.getClient(); client != nil {
			total, err := s.store.PlaylistTrackCount(playlistID)
			if err != nil {
				return err
			}
			if total > 0 {
				existing, err := s.store.PlaylistTracks(playlistID, total, 0)
				if err != nil {
					return err
				}
				drop := make(map[string]struct{}, len(refs))
				for _, ref := range refs {
					drop[ref] = struct{}{}
				}
				var idxs []int
				for i, tr := range existing {
					if _, ok := drop[tr.Ref]; ok {
						idxs = append(idxs, i)
					}
				}
				if len(idxs) > 0 {
					if err := client.UpdatePlaylist(ctx, remoteID, nil, nil, idxs); err != nil {
						return err
					}
				}
			}
		}
	}

	return s.store.RemoveFromPlaylist(playlistID, refs)
}

// RenamePlaylist renames a playlist on Navidrome and in SQLite.
func (s *Source) RenamePlaylist(ctx context.Context, playlistID, name string) error {
	remoteID, err := s.remotePlaylistID(playlistID)
	if err != nil {
		return err
	}
	if remoteID != "" {
		if client := s.getClient(); client != nil {
			if err := client.UpdatePlaylist(ctx, remoteID, &name, nil, nil); err != nil {
				return err
			}
		}
	}

	return s.store.RenamePlaylist(s.lib.ID, playlistID, name)
}

// DeletePlaylist deletes a playlist from Navidrome and SQLite.
func (s *Source) DeletePlaylist(ctx context.Context, playlistID string) error {
	remoteID, err := s.remotePlaylistID(playlistID)
	if err != nil {
		return err
	}
	if remoteID != "" {
		if client := s.getClient(); client != nil {
			if err := client.DeletePlaylist(ctx, remoteID); err != nil {
				return err
			}
		}
	}

	return s.store.DeletePlaylist(s.lib.ID, playlistID)
}

// Sync fetches all albums, songs, starred states, cover art, and playlists from
// Navidrome and reconciles them into SQLite.
func (s *Source) Sync(ctx context.Context) (int, error) {
	client := s.getClient()
	if client == nil {
		err := fmt.Errorf("navidrome: client not configured")
		s.setStatus(library.StatusError, err.Error())

		return 0, err
	}
	s.setStatus(library.StatusScanning, "")
	if s.onChange != nil {
		s.onChange(SyncProgress{LibraryID: s.lib.ID, Done: 0, Total: 0})
	}

	var albums []nd.Album
	for offset := 0; ; offset += syncAlbumPageSize {
		if ctx.Err() != nil {
			s.setStatus(library.StatusError, ctx.Err().Error())

			return 0, ctx.Err()
		}
		page, err := client.AlbumList(ctx, syncAlbumPageSize, offset)
		if err != nil {
			s.setStatus(library.StatusError, err.Error())

			return 0, err
		}
		albums = append(albums, page...)
		if len(page) < syncAlbumPageSize {
			break
		}
	}

	albumNames := make(map[string]string, len(albums))
	albumArtists := make(map[string]string, len(albums))
	albumCovers := make(map[string]string, len(albums))
	albumArtistsByName := make(map[string]string, len(albums))
	albumCoversByName := make(map[string]string, len(albums))
	for _, al := range albums {
		title := al.Name
		if title == "" {
			title = al.Title
		}
		if title != "" {
			albumNames[al.ID] = title
			lower := strings.ToLower(title)
			if _, exists := albumArtistsByName[lower]; !exists && al.Artist != "" {
				albumArtistsByName[lower] = al.Artist
			}
			if _, exists := albumCoversByName[lower]; !exists && al.CoverArt != "" {
				albumCoversByName[lower] = al.CoverArt
			}
		}
		if al.Artist != "" {
			albumArtists[al.ID] = al.Artist
		}
		if al.CoverArt != "" {
			albumCovers[al.ID] = al.CoverArt
		}
	}

	// Fast path: Navidrome / OpenSubsonic supports empty query in search3 to
	// paginate all songs directly (500 per request instead of 1 request per album).
	var songs []nd.Song
	for offset := 0; ; offset += syncSongPageSize {
		if ctx.Err() != nil {
			s.setStatus(library.StatusError, ctx.Err().Error())

			return 0, ctx.Err()
		}
		page, err := s.searchSongsPage(ctx, client, syncSongPageSize, offset)
		if err != nil {
			songs = nil

			break
		}
		songs = append(songs, page...)
		if s.onChange != nil {
			s.onChange(SyncProgress{LibraryID: s.lib.ID, Done: len(songs), Total: len(songs)})
		}
		if len(page) < syncSongPageSize {
			break
		}
	}

	// Fallback for strict Subsonic servers that return empty results for query="".
	if len(songs) == 0 && len(albums) > 0 {
		type albumResult struct {
			album nd.Album
			err   error
		}
		albumResults := make([]albumResult, len(albums))
		albumCh := make(chan int)
		var albumWG sync.WaitGroup
		for i := 0; i < syncAlbumWorkers; i++ {
			albumWG.Add(1)
			go func() {
				defer albumWG.Done()
				for idx := range albumCh {
					al, err := client.GetAlbum(ctx, albums[idx].ID)
					albumResults[idx] = albumResult{album: al, err: err}
				}
			}()
		}
		for i := range albums {
			if ctx.Err() != nil {
				break
			}
			albumCh <- i
		}
		close(albumCh)
		albumWG.Wait()

		if ctx.Err() != nil {
			s.setStatus(library.StatusError, ctx.Err().Error())

			return 0, ctx.Err()
		}

		songs = make([]nd.Song, 0, len(albums)*10)
		for _, res := range albumResults {
			if res.err != nil {
				s.setStatus(library.StatusError, res.err.Error())

				return 0, res.err
			}
			al := res.album
			albumTitle := al.Name
			if albumTitle == "" {
				albumTitle = al.Title
			}
			if albumTitle == "" {
				albumTitle = albumNames[al.ID]
			}
			if albumTitle != "" {
				albumNames[al.ID] = albumTitle
				lower := strings.ToLower(albumTitle)
				if _, exists := albumArtistsByName[lower]; !exists && al.Artist != "" {
					albumArtistsByName[lower] = al.Artist
				}
				if _, exists := albumCoversByName[lower]; !exists && al.CoverArt != "" {
					albumCoversByName[lower] = al.CoverArt
				}
			}
			if al.Artist != "" {
				albumArtists[al.ID] = al.Artist
			}
			if al.CoverArt != "" {
				albumCovers[al.ID] = al.CoverArt
			}
			for idx := range al.Songs {
				song := &al.Songs[idx]
				if song.Album == "" {
					song.Album = albumTitle
				}
				if song.AlbumID == "" {
					song.AlbumID = al.ID
				}
				if song.Artist == "" && al.Artist != "" {
					song.Artist = al.Artist
				}
				if song.CoverArt == "" && al.CoverArt != "" {
					song.CoverArt = al.CoverArt
				}
				if song.Year == 0 && al.Year != 0 {
					song.Year = al.Year
				}
				if song.Genre == "" && al.Genre != "" {
					song.Genre = al.Genre
				}
			}
			songs = append(songs, al.Songs...)
		}
	}

	// Backfill album info, parent IDs, and album covers on all songs.
	for idx := range songs {
		song := &songs[idx]
		if song.AlbumID == "" && song.Parent != "" {
			song.AlbumID = song.Parent
		}
		if song.Album == "" && song.AlbumID != "" {
			song.Album = albumNames[song.AlbumID]
		}
		if song.CoverArt == "" && song.AlbumID != "" {
			song.CoverArt = albumCovers[song.AlbumID]
		}
		if song.CoverArt == "" && song.Album != "" {
			song.CoverArt = albumCoversByName[strings.ToLower(song.Album)]
		}
	}

	// Build a lookup of existing cover_ids in SQLite so untouched tracks reuse
	// artwork already stored on disk without hitting getCoverArt again.
	existingCovers := s.existingTrackCovers()

	distinctCovers := make(map[string]struct{})
	resolvedCovers := make(map[string]string)
	for _, song := range songs {
		if song.CoverArt == "" {
			continue
		}
		native := song.ID
		if cid := existingCovers[native]; cid != "" && s.art != nil && s.art.Has(cid) {
			resolvedCovers[song.CoverArt] = cid

			continue
		}
		if _, done := resolvedCovers[song.CoverArt]; !done {
			distinctCovers[song.CoverArt] = struct{}{}
		}
	}
	for _, coverArt := range albumCovers {
		if coverArt == "" {
			continue
		}
		if _, done := resolvedCovers[coverArt]; !done {
			distinctCovers[coverArt] = struct{}{}
		}
	}

	if s.art != nil && len(distinctCovers) > 0 {
		coverKeys := make([]string, 0, len(distinctCovers))
		for k := range distinctCovers {
			coverKeys = append(coverKeys, k)
		}
		coverVals := make([]string, len(coverKeys))
		coverCh := make(chan int)
		var coverWG sync.WaitGroup
		for i := 0; i < syncCoverWorkers; i++ {
			coverWG.Add(1)
			go func() {
				defer coverWG.Done()
				for idx := range coverCh {
					cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
					data, _, err := client.GetCoverArt(cctx, coverKeys[idx], coverArtSize)
					cancel()
					if err == nil && len(data) > 0 {
						if cid, err := s.art.Put(data); err == nil {
							coverVals[idx] = cid
						}
					}
				}
			}()
		}
		for i := range coverKeys {
			if ctx.Err() != nil {
				break
			}
			coverCh <- i
		}
		close(coverCh)
		coverWG.Wait()
		for i, k := range coverKeys {
			if coverVals[i] != "" {
				resolvedCovers[k] = coverVals[i]
			}
		}
	}

	keepNative := make(map[string]struct{}, len(songs))
	for i, song := range songs {
		if err := ctx.Err(); err != nil {
			s.setStatus(library.StatusError, err.Error())

			return i, err
		}
		native := song.ID
		keepNative[native] = struct{}{}

		albumArtist := albumArtists[song.AlbumID]
		if albumArtist == "" && song.Album != "" {
			albumArtist = albumArtistsByName[strings.ToLower(song.Album)]
		}
		if albumArtist == "" {
			albumArtist = song.Artist
		}
		coverID := resolvedCovers[song.CoverArt]
		if coverID == "" && song.AlbumID != "" {
			coverID = resolvedCovers[albumCovers[song.AlbumID]]
		}
		if coverID == "" && song.Album != "" {
			coverID = resolvedCovers[albumCoversByName[strings.ToLower(song.Album)]]
		}
		if _, err := s.store.UpsertRemoteTrack(s.lib.ID, library.KindNavidrome, sqlite.TrackRow{
			NativeID:     native,
			Title:        song.Title,
			Artist:       song.Artist,
			Album:        song.Album,
			AlbumArtist:  albumArtist,
			Genre:        song.Genre,
			Year:         song.Year,
			TrackNo:      song.Track,
			DiscNo:       song.DiscNumber,
			DurationMs:   int64(song.Duration) * 1000,
			CoverID:      coverID,
			ThumbnailURL: song.CoverArt,
			ArtistID:     song.ArtistID,
		}); err != nil {
			s.setStatus(library.StatusError, err.Error())

			return i, err
		}
		_ = s.store.SetLiked(library.ScopedRemoteRef(library.KindNavidrome, s.lib.ID, native), song.Starred != "")
		if s.onChange != nil && (i%25 == 0 || i == len(songs)-1) {
			s.onChange(SyncProgress{LibraryID: s.lib.ID, Done: i + 1, Total: len(songs)})
		}
	}

	if err := s.store.DeleteMissingRemoteTracks(s.lib.ID, library.KindNavidrome, keepNative); err != nil {
		s.setStatus(library.StatusError, err.Error())

		return len(songs), err
	}

	if err := s.syncPlaylists(ctx, client); err != nil {
		s.setStatus(library.StatusError, err.Error())

		return len(songs), err
	}

	if err := s.store.TouchScan(s.lib.ID); err != nil {
		s.setStatus(library.StatusError, err.Error())

		return len(songs), err
	}
	s.setStatus(library.StatusReady, "")

	return len(songs), nil
}

func (s *Source) searchSongsPage(ctx context.Context, client *nd.Client, count, offset int) ([]nd.Song, error) {
	return client.SearchSongs(ctx, "", count, offset)
}

func (s *Source) syncPlaylists(ctx context.Context, client *nd.Client) error {
	remoteLists, err := client.GetPlaylists(ctx)
	if err != nil {
		return err
	}
	keepRemote := make(map[string]bool, len(remoteLists))
	for _, meta := range remoteLists {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		keepRemote[meta.ID] = true
		full, err := client.GetPlaylist(ctx, meta.ID)
		if err != nil {
			continue
		}
		pl, found, err := s.store.PlaylistByRemoteID(s.lib.ID, meta.ID)
		if err != nil {
			return err
		}
		if !found {
			pl, err = s.store.CreatePlaylistWithRemote(s.lib.ID, full.Name, meta.ID)
			if err != nil {
				return err
			}
		} else if full.Name != "" && pl.Name != full.Name {
			_ = s.store.RenamePlaylist(s.lib.ID, pl.ID, full.Name)
		}

		refs := make([]string, 0, len(full.Entries))
		for _, entry := range full.Entries {
			refs = append(refs, library.ScopedRemoteRef(library.KindNavidrome, s.lib.ID, entry.ID))
		}
		if err := s.store.ReplacePlaylistEntries(pl.ID, refs); err != nil {
			return err
		}
		if covers, err := s.store.PlaylistCoverIDs(pl.ID, 4); err == nil && len(covers) > 0 && s.art != nil {
			// Best-effort single/composite cover update happens in PlayerService.
			_ = s.store.SetPlaylistCover(pl.ID, covers[0], false)
		}
	}

	return s.store.PruneRemotePlaylists(s.lib.ID, keepRemote)
}

func (s *Source) existingTrackCovers() map[string]string {
	out := make(map[string]string)
	for offset := 0; ; offset += 500 {
		tracks, total, err := s.store.Tracks(s.lib.ID, 500, offset)
		if err != nil || len(tracks) == 0 {
			break
		}
		for _, tr := range tracks {
			if tr.CoverID != "" {
				out[tr.NativeID] = tr.CoverID
			}
		}
		if offset+len(tracks) >= total {
			break
		}
	}

	return out
}

func nextCursor(next, total int) string {
	if next >= total {
		return ""
	}

	return strconv.Itoa(next)
}
