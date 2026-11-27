package sqlite

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dlcuy22/molow/internal/library"
)

// refSQL derives a row's playable ref. A local track is played by its file
// path, which track_file owns so a moved file resolves to its new path without
// touching a stored ref; a missing file has no ref, which the caller treats as
// unplayable. Remote tracks in a remote library emit an instance-scoped ref
// ("<kind>:<libraryId>:<nativeId>"), while remote tracks imported into a local
// library emit an unscoped ref ("<kind>:<nativeId>").
const refSQL = `CASE
	WHEN t.kind = 'local' THEN COALESCE((SELECT f.path FROM track_file f
	                                     WHERE f.track_id = t.id AND f.missing = 0
	                                     ORDER BY f.path LIMIT 1), '')
	WHEN t.kind <> '' AND lib.kind <> 'local' THEN t.kind || ':' || t.library_id || ':' || t.native_id
	WHEN t.kind <> '' THEN t.kind || ':' || t.native_id
	ELSE ''
END`

// trackColumns is the one projection every track query uses.
const trackColumns = `
	t.library_id, t.kind, t.native_id, t.title, t.artist, t.album, t.album_artist,
	t.genre, t.year, t.track_no, t.disc_no, t.duration_ms, t.cover_id,
	` + refSQL + ` AS ref,
	(l.ref IS NOT NULL) AS liked`

const trackFrom = ` FROM track t
	JOIN library lib ON lib.id = t.library_id
	LEFT JOIN like l ON l.ref = (` + refSQL + `)`

func scanTrack(rows *sql.Rows) (library.Track, error) {
	var t library.Track
	err := rows.Scan(
		&t.LibraryID, &t.Kind, &t.NativeID, &t.Title, &t.Artist, &t.Album,
		&t.AlbumArtist, &t.Genre, &t.Year, &t.TrackNo, &t.DiscNo, &t.DurationMs,
		&t.CoverID, &t.Ref, &t.Liked,
	)
	t.Flags = library.FlagPlayable
	if t.Ref == "" {
		t.Flags = 0
	}

	return t, err
}

// Tracks returns the catalogue in album order: artist, album, disc, track.
func (s *Store) Tracks(libraryID string, limit, offset int) ([]library.Track, int, error) {
	total, err := s.count(`SELECT COUNT(*) FROM track t WHERE t.library_id = ?`, libraryID)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(
		`SELECT`+trackColumns+trackFrom+
			` WHERE t.library_id = ?
			 ORDER BY t.artist COLLATE NOCASE, t.album COLLATE NOCASE,
			          COALESCE(NULLIF(t.disc_no, 0), 1),
			          CASE WHEN t.track_no > 0 THEN 0 ELSE 1 END,
			          t.track_no, t.title COLLATE NOCASE
			 LIMIT ? OFFSET ?`,
		libraryID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	return collectTracks(rows, total)
}

// RecentTracks is the home page's newest-first list.
func (s *Store) RecentTracks(libraryID string, limit int) ([]library.Track, error) {
	rows, err := s.db.Query(
		`SELECT`+trackColumns+trackFrom+
			` WHERE t.library_id = ?
			 ORDER BY t.added_at DESC, t.id DESC LIMIT ?`,
		libraryID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, _, err := collectTracks(rows, limit)

	return out, err
}

// Albums groups the catalogue by album and album artist. An album with no
// album-artist tag groups under its track artist, which is what a compilation
// without the tag actually is.
func (s *Store) Albums(libraryID string, limit, offset int) ([]library.Album, int, error) {
	total, err := s.count(
		`SELECT COUNT(*) FROM (
		   SELECT 1 FROM track WHERE library_id = ? AND album <> ''
		   GROUP BY album, COALESCE(NULLIF(album_artist, ''), artist)
		 )`, libraryID,
	)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(
		`SELECT t.album,
		        COALESCE(NULLIF(t.album_artist, ''), t.artist) AS album_artist,
		        COUNT(*), COALESCE(SUM(t.duration_ms), 0),
		        MIN(NULLIF(t.year, 0)),
		        COALESCE((SELECT t2.cover_id FROM track t2
		                  WHERE t2.library_id = t.library_id
		                    AND t2.album = t.album
		                    AND COALESCE(NULLIF(t2.album_artist, ''), t2.artist) = COALESCE(NULLIF(t.album_artist, ''), t.artist)
		                    AND t2.cover_id <> ''
		                  LIMIT 1), '') AS cover_id
		 FROM track t
		 WHERE t.library_id = ? AND t.album <> ''
		 GROUP BY t.album, COALESCE(NULLIF(t.album_artist, ''), t.artist)
		 ORDER BY MAX(t.added_at) DESC, t.album COLLATE NOCASE
		 LIMIT ? OFFSET ?`,
		libraryID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []library.Album
	for rows.Next() {
		var (
			a     library.Album
			year  sql.NullInt64
			count int
		)
		if err := rows.Scan(&a.Name, &a.Artist, &count, &a.DurationMs, &year, &a.CoverID); err != nil {
			return nil, 0, err
		}
		a.LibraryID = libraryID
		a.SongCount = count
		if year.Valid {
			a.Year = int(year.Int64)
		}
		a.ID = albumID(libraryID, a.Name, a.Artist)
		out = append(out, a)
	}

	return out, total, rows.Err()
}

// AlbumTracks returns one album in disc and track order, treating disc_no 0 as disc 1.
func (s *Store) AlbumTracks(libraryID, album, artist string) ([]library.Track, error) {
	rows, err := s.db.Query(
		`SELECT`+trackColumns+trackFrom+
			` WHERE t.library_id = ? AND t.album = ?
			   AND COALESCE(NULLIF(t.album_artist, ''), t.artist) = ?
			 ORDER BY COALESCE(NULLIF(t.disc_no, 0), 1),
			          CASE WHEN t.track_no > 0 THEN 0 ELSE 1 END,
			          t.track_no, t.title COLLATE NOCASE`,
		libraryID, album, artist,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, _, err := collectTracks(rows, 0)

	return out, err
}

// ArtistTracks returns every track by one artist in album and track order.
func (s *Store) ArtistTracks(libraryID, artist string) ([]library.Track, error) {
	rows, err := s.db.Query(
		`SELECT`+trackColumns+trackFrom+
			` WHERE t.library_id = ? AND t.artist = ?
			 ORDER BY t.album COLLATE NOCASE,
			          COALESCE(NULLIF(t.disc_no, 0), 1),
			          CASE WHEN t.track_no > 0 THEN 0 ELSE 1 END,
			          t.track_no, t.title COLLATE NOCASE`,
		libraryID, artist,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, _, err := collectTracks(rows, 0)

	return out, err
}

// Artists groups the catalogue by artist name.
func (s *Store) Artists(libraryID string, limit, offset int) ([]library.Artist, int, error) {
	total, err := s.count(
		`SELECT COUNT(DISTINCT artist) FROM track WHERE library_id = ? AND artist <> ''`, libraryID,
	)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(
		`SELECT t.artist, COUNT(*), COUNT(DISTINCT t.album),
		        COALESCE((SELECT t2.cover_id FROM track t2
		                  WHERE t2.library_id = t.library_id AND t2.artist = t.artist
		                    AND t2.cover_id <> '' LIMIT 1), '')
		 FROM track t
		 WHERE t.library_id = ? AND t.artist <> ''
		 GROUP BY t.artist
		 ORDER BY t.artist COLLATE NOCASE
		 LIMIT ? OFFSET ?`,
		libraryID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []library.Artist
	for rows.Next() {
		var a library.Artist
		if err := rows.Scan(&a.Name, &a.SongCount, &a.AlbumCount, &a.CoverID); err != nil {
			return nil, 0, err
		}
		a.LibraryID = libraryID
		a.ID = libraryID + "\x00" + a.Name
		out = append(out, a)
	}

	return out, total, rows.Err()
}

// Search runs the FTS index. Each term is quoted, so punctuation in a query is
// a literal rather than FTS syntax.
func (s *Store) Search(libraryID, query string, limit int) ([]library.Track, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT`+trackColumns+
			` FROM track t
			  JOIN library lib ON lib.id = t.library_id
			  JOIN track_fts ON track_fts.rowid = t.id
			  LEFT JOIN like l ON l.ref = (`+refSQL+`)
			  WHERE t.library_id = ? AND track_fts MATCH ?
			  ORDER BY rank LIMIT ?`,
		libraryID, match, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, _, err := collectTracks(rows, limit)

	return out, err
}

// rowQuerier is the read surface both a store and a transaction expose, so a
// ref resolves the same way inside and outside a transaction.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// refTrackIDInLibrary resolves ref to its track row strictly within libraryID,
// ensuring playlists in one library never link to tracks owned by another.
func refTrackIDInLibrary(q rowQuerier, libraryID, ref string) (int64, error) {
	var id int64
	if kind, refLibID, native, ok := library.ParseRemoteRef(ref); ok {
		if refLibID != "" && refLibID != libraryID {
			return 0, sql.ErrNoRows
		}
		err := q.QueryRow(
			`SELECT id FROM track WHERE library_id = ? AND kind = ? AND native_id = ? LIMIT 1`,
			libraryID, string(kind), native,
		).Scan(&id)

		return id, err
	}
	err := q.QueryRow(
		`SELECT track_id FROM track_file WHERE library_id = ? AND path = ? LIMIT 1`,
		libraryID, ref,
	).Scan(&id)

	return id, err
}

// TrackByRef resolves a playable ref back to its catalogue row. Scoped refs
// match strictly by owning library_id; unscoped remote refs prefer a local
// import row before falling back to remote libraries.
func (s *Store) TrackByRef(ref string) (library.Track, bool, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if kind, libID, native, ok := library.ParseRemoteRef(ref); ok {
		if libID != "" {
			rows, err = s.db.Query(
				`SELECT`+trackColumns+trackFrom+
					` WHERE t.library_id = ? AND t.kind = ? AND t.native_id = ? ORDER BY t.id LIMIT 1`,
				libID, string(kind), native,
			)
		} else {
			rows, err = s.db.Query(
				`SELECT`+trackColumns+trackFrom+
					` WHERE t.kind = ? AND t.native_id = ?
					  ORDER BY (CASE WHEN lib.kind = 'local' THEN 0 ELSE 1 END), t.id LIMIT 1`,
				string(kind), native,
			)
		}
	} else {
		rows, err = s.db.Query(
			`SELECT`+trackColumns+
				` FROM track t
				  JOIN library lib ON lib.id = t.library_id
				  JOIN track_file f ON f.track_id = t.id AND f.path = ?
				  LEFT JOIN like l ON l.ref = (`+refSQL+`)
				  ORDER BY t.id LIMIT 1`, ref,
		)
	}
	if err != nil {
		return library.Track{}, false, err
	}
	defer rows.Close()
	out, _, err := collectTracks(rows, 1)
	if err != nil || len(out) == 0 {
		return library.Track{}, false, err
	}

	return out[0], true, nil
}

// SetLiked inserts or removes a global like for ref.
func (s *Store) SetLiked(ref string, liked bool) error {
	if ref == "" {
		return fmt.Errorf("library: empty ref")
	}
	if liked {
		_, err := s.db.Exec(
			`INSERT INTO like (ref, created_at) VALUES (?, ?)
			 ON CONFLICT(ref) DO NOTHING`, ref, time.Now().Unix(),
		)

		return err
	}
	_, err := s.db.Exec(`DELETE FROM like WHERE ref = ?`, ref)

	return err
}

// Liked reports whether ref carries a global like.
func (s *Store) Liked(ref string) (bool, error) {
	if ref == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM like WHERE ref = ?`, ref).Scan(&n)

	return n > 0, err
}

// ToggleLiked flips one global like and returns the state it now has.
func (s *Store) ToggleLiked(ref string) (bool, error) {
	if ref == "" {
		return false, fmt.Errorf("library: empty ref")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM like WHERE ref = ?`, ref).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		_, err := s.db.Exec(`DELETE FROM like WHERE ref = ?`, ref)

		return false, err
	}
	_, err := s.db.Exec(`INSERT INTO like (ref, created_at) VALUES (?, ?)`, ref, time.Now().Unix())

	return true, err
}

// Playlists lists a library's playlists with their entry counts.
func (s *Store) Playlists(libraryID string) ([]library.Playlist, error) {
	rows, err := s.db.Query(
		`SELECT p.id, p.name, p.description, p.remote_id, p.source_path, p.watched,
		        p.cover_id, p.custom_cover, p.owned,
		        (SELECT COUNT(*) FROM playlist_entry e WHERE e.playlist_id = p.id)
		 FROM playlist p WHERE p.library_id = ?
		 ORDER BY p.updated_at DESC, p.name COLLATE NOCASE`,
		libraryID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []library.Playlist
	for rows.Next() {
		var (
			p           library.Playlist
			watched     int
			customCover int
			owned       int
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.RemoteID, &p.SourcePath, &watched, &p.CoverID, &customCover, &owned, &p.TrackCount); err != nil {
			return nil, err
		}
		p.LibraryID = libraryID
		p.Watched = watched != 0
		p.CustomCover = customCover != 0
		p.Owned = owned != 0
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Close the outer cursor before running follow-up queries on the single SQLite connection.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].CoverID != "" {
			continue
		}
		covers, err := s.PlaylistCoverIDs(out[i].ID, 4)
		if err != nil {
			return nil, err
		}
		out[i].CoverIDs = covers
	}

	return out, nil
}

// PlaylistCoverIDs returns up to limit distinct non-empty track cover ids in playlist order.
func (s *Store) PlaylistCoverIDs(playlistID string, limit int) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT t.cover_id
		 FROM playlist_entry e
		 JOIN track t ON t.id = e.track_id
		 WHERE e.playlist_id = ? AND t.cover_id <> ''
		 ORDER BY e.position`,
		playlistID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]struct{}, limit)
	var out []string
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		if _, dup := seen[cid]; dup {
			continue
		}
		seen[cid] = struct{}{}
		out = append(out, cid)
		if len(out) == limit {
			break
		}
	}

	return out, rows.Err()
}

// SetPlaylistCover updates a playlist's stored cover_id. When custom is false,
// rows with custom_cover = 1 are left untouched so automatic mutations never
// overwrite user-selected artwork.
func (s *Store) SetPlaylistCover(playlistID, coverID string, custom bool) error {
	if custom {
		_, err := s.db.Exec(
			`UPDATE playlist SET cover_id = ?, custom_cover = 1 WHERE id = ?`,
			coverID, playlistID,
		)

		return err
	}
	_, err := s.db.Exec(
		`UPDATE playlist SET cover_id = ? WHERE id = ? AND custom_cover = 0`,
		coverID, playlistID,
	)

	return err
}

// PlaylistTracks reads a playlist in position order.
func (s *Store) PlaylistTracks(playlistID string, limit, offset int) ([]library.Track, error) {
	rows, err := s.db.Query(
		`SELECT`+trackColumns+
			` FROM playlist_entry e
			  JOIN track t ON t.id = e.track_id
			  JOIN library lib ON lib.id = t.library_id
			  LEFT JOIN like l ON l.ref = (`+refSQL+`)
			  WHERE e.playlist_id = ?
			  ORDER BY e.position LIMIT ? OFFSET ?`,
		playlistID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, _, err := collectTracks(rows, limit)

	return out, err
}

// PlaylistTrackCount reports how many entries a playlist holds.
func (s *Store) PlaylistTrackCount(playlistID string) (int, error) {
	return s.count(`SELECT COUNT(*) FROM playlist_entry WHERE playlist_id = ?`, playlistID)
}

// CreatePlaylist adds one and returns it.
func (s *Store) CreatePlaylist(libraryID, name string) (library.Playlist, error) {
	return s.createPlaylist(libraryID, name, "", "", false, true)
}

// CreatePlaylistWithRemote adds a playlist that mirrors a remote list, so the
// row remembers which list it came from. Imported local remote playlists start
// watched so sync picks up upstream changes.
func (s *Store) CreatePlaylistWithRemote(libraryID, name, remoteID string) (library.Playlist, error) {
	return s.createPlaylist(libraryID, name, remoteID, "", remoteID != "", true)
}

// CreatePlaylistWithSource adds a playlist backed by a watched local folder.
func (s *Store) CreatePlaylistWithSource(libraryID, name, sourcePath string, watched bool) (library.Playlist, error) {
	return s.createPlaylist(libraryID, name, "", sourcePath, watched, true)
}

func (s *Store) createPlaylistWithRemoteOwned(libraryID, name, remoteID string, owned bool) (library.Playlist, error) {
	return s.createPlaylist(libraryID, name, remoteID, "", false, owned)
}

func (s *Store) createPlaylist(libraryID, name, remoteID, sourcePath string, watched, owned bool) (library.Playlist, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return library.Playlist{}, err
	}
	id := "playlist:" + hex.EncodeToString(raw[:])
	now := time.Now().Unix()
	ownedInt := 0
	if owned {
		ownedInt = 1
	}
	watchedInt := 0
	if watched {
		watchedInt = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO playlist (id, library_id, name, remote_id, source_path, watched, owned, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, libraryID, name, remoteID, sourcePath, watchedInt, ownedInt, now, now,
	)
	if err != nil {
		return library.Playlist{}, err
	}

	return library.Playlist{
		ID:         id,
		LibraryID:  libraryID,
		Name:       name,
		RemoteID:   remoteID,
		SourcePath: sourcePath,
		Watched:    watched,
		Owned:      owned,
	}, nil
}

// UpsertRemotePlaylist finds a playlist by (libraryID, remoteID), updating its
// name and ownership if present or inserting a new row when missing.
func (s *Store) UpsertRemotePlaylist(libraryID, name, remoteID string, owned bool) (library.Playlist, error) {
	if remoteID == "" {
		return s.CreatePlaylist(libraryID, name)
	}
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM playlist WHERE library_id = ? AND remote_id = ? LIMIT 1`,
		libraryID, remoteID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return s.createPlaylistWithRemoteOwned(libraryID, name, remoteID, owned)
	}
	if err != nil {
		return library.Playlist{}, err
	}
	ownedInt := 0
	if owned {
		ownedInt = 1
	}
	if _, err := s.db.Exec(
		`UPDATE playlist SET name = ?, owned = ?, updated_at = ? WHERE id = ?`,
		name, ownedInt, time.Now().Unix(), id,
	); err != nil {
		return library.Playlist{}, err
	}
	return library.Playlist{ID: id, LibraryID: libraryID, Name: name, RemoteID: remoteID, Owned: owned}, nil
}

// PlaylistByID returns the playlist row matching (libraryID, playlistID).
func (s *Store) PlaylistByID(libraryID, playlistID string) (library.Playlist, bool, error) {
	return s.scanSinglePlaylist(
		`SELECT p.id, p.name, p.description, p.remote_id, p.source_path, p.watched,
		        p.cover_id, p.custom_cover, p.owned,
		        (SELECT COUNT(*) FROM playlist_entry e WHERE e.playlist_id = p.id)
		 FROM playlist p WHERE p.library_id = ? AND p.id = ? LIMIT 1`,
		libraryID, playlistID,
	)
}

// PlaylistBySourcePath returns the playlist row backed by sourcePath in libraryID.
func (s *Store) PlaylistBySourcePath(libraryID, sourcePath string) (library.Playlist, bool, error) {
	return s.scanSinglePlaylist(
		`SELECT p.id, p.name, p.description, p.remote_id, p.source_path, p.watched,
		        p.cover_id, p.custom_cover, p.owned,
		        (SELECT COUNT(*) FROM playlist_entry e WHERE e.playlist_id = p.id)
		 FROM playlist p WHERE p.library_id = ? AND p.source_path = ? LIMIT 1`,
		libraryID, sourcePath,
	)
}

// PlaylistByRemoteID returns the mirrored playlist row matching (libraryID, remoteID).
func (s *Store) PlaylistByRemoteID(libraryID, remoteID string) (library.Playlist, bool, error) {
	return s.scanSinglePlaylist(
		`SELECT p.id, p.name, p.description, p.remote_id, p.source_path, p.watched,
		        p.cover_id, p.custom_cover, p.owned,
		        (SELECT COUNT(*) FROM playlist_entry e WHERE e.playlist_id = p.id)
		 FROM playlist p WHERE p.library_id = ? AND p.remote_id = ? LIMIT 1`,
		libraryID, remoteID,
	)
}

func (s *Store) scanSinglePlaylist(query, libraryID, arg string) (library.Playlist, bool, error) {
	var (
		p           library.Playlist
		watched     int
		customCover int
		owned       int
	)
	err := s.db.QueryRow(query, libraryID, arg).Scan(
		&p.ID, &p.Name, &p.Description, &p.RemoteID, &p.SourcePath, &watched,
		&p.CoverID, &customCover, &owned, &p.TrackCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return library.Playlist{}, false, nil
	}
	if err != nil {
		return library.Playlist{}, false, err
	}
	p.LibraryID = libraryID
	p.Watched = watched != 0
	p.CustomCover = customCover != 0
	p.Owned = owned != 0
	return p, true, nil
}

// SetPlaylistWatched toggles automatic watching for a folder-backed or remote-backed
// playlist and keeps any matching library_root row in sync.
func (s *Store) SetPlaylistWatched(libraryID, playlistID string, watched bool) (library.Playlist, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return library.Playlist{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var sourcePath, remoteID string
	err = tx.QueryRow(
		`SELECT source_path, remote_id FROM playlist WHERE library_id = ? AND id = ? LIMIT 1`,
		libraryID, playlistID,
	).Scan(&sourcePath, &remoteID)
	if errors.Is(err, sql.ErrNoRows) {
		return library.Playlist{}, fmt.Errorf("playlist: unknown id %q", playlistID)
	}
	if err != nil {
		return library.Playlist{}, err
	}
	if sourcePath == "" && remoteID == "" {
		return library.Playlist{}, fmt.Errorf("playlist: %q has no watched source", playlistID)
	}

	w := 0
	if watched {
		w = 1
	}
	if _, err := tx.Exec(
		`UPDATE playlist SET watched = ?, updated_at = ? WHERE library_id = ? AND id = ?`,
		w, time.Now().Unix(), libraryID, playlistID,
	); err != nil {
		return library.Playlist{}, err
	}
	if sourcePath != "" {
		if _, err := tx.Exec(
			`UPDATE library_root SET watched = ? WHERE library_id = ? AND path = ?`,
			w, libraryID, sourcePath,
		); err != nil {
			return library.Playlist{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return library.Playlist{}, err
	}

	pl, _, err := s.PlaylistByID(libraryID, playlistID)
	return pl, err
}

// WatchedRemotePlaylists returns watched imported remote playlists in libraryID.
func (s *Store) WatchedRemotePlaylists(libraryID string) ([]library.Playlist, error) {
	return s.queryPlaylists(
		`SELECT p.id, p.name, p.description, p.remote_id, p.source_path, p.watched,
		        p.cover_id, p.custom_cover, p.owned,
		        (SELECT COUNT(*) FROM playlist_entry e WHERE e.playlist_id = p.id)
		 FROM playlist p WHERE p.library_id = ? AND p.remote_id <> '' AND p.watched = 1
		 ORDER BY p.rowid`,
		libraryID,
	)
}

// WatchedFolderPlaylists returns watched folder-backed playlists in libraryID.
func (s *Store) WatchedFolderPlaylists(libraryID string) ([]library.Playlist, error) {
	return s.queryPlaylists(
		`SELECT p.id, p.name, p.description, p.remote_id, p.source_path, p.watched,
		        p.cover_id, p.custom_cover, p.owned,
		        (SELECT COUNT(*) FROM playlist_entry e WHERE e.playlist_id = p.id)
		 FROM playlist p WHERE p.library_id = ? AND p.source_path <> '' AND p.watched = 1
		 ORDER BY p.rowid`,
		libraryID,
	)
}

func (s *Store) queryPlaylists(query, libraryID string) ([]library.Playlist, error) {
	rows, err := s.db.Query(query, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []library.Playlist
	for rows.Next() {
		var (
			p           library.Playlist
			watched     int
			customCover int
			owned       int
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.RemoteID, &p.SourcePath, &watched, &p.CoverID, &customCover, &owned, &p.TrackCount); err != nil {
			return nil, err
		}
		p.LibraryID = libraryID
		p.Watched = watched != 0
		p.CustomCover = customCover != 0
		p.Owned = owned != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// LinkLegacyFolderPlaylist attaches source_path and root watch state to an
// existing folder-import playlist created before migration 008.
func (s *Store) LinkLegacyFolderPlaylist(libraryID, root, baseName string) error {
	if root == "" || baseName == "" {
		return nil
	}
	if _, ok, err := s.PlaylistBySourcePath(libraryID, root); err != nil || ok {
		return err
	}

	var id string
	err := s.db.QueryRow(
		`SELECT id FROM playlist
		 WHERE library_id = ? AND source_path = '' AND remote_id = '' AND name = ?
		 ORDER BY rowid LIMIT 1`,
		libraryID, baseName,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	watched := 1
	_ = s.db.QueryRow(
		`SELECT watched FROM library_root WHERE library_id = ? AND path = ?`,
		libraryID, root,
	).Scan(&watched)

	_, err = s.db.Exec(
		`UPDATE playlist SET source_path = ?, watched = ? WHERE id = ?`,
		root, watched, id,
	)
	return err
}

// ReplacePlaylistEntries atomically replaces all entries in playlistID with refs
// in dense 0..N-1 position order, skipping unresolved or duplicate track IDs.
func (s *Store) ReplacePlaylistEntries(playlistID string, refs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var libraryID string
	if err := tx.QueryRow(`SELECT library_id FROM playlist WHERE id = ?`, playlistID).Scan(&libraryID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM playlist_entry WHERE playlist_id = ?`, playlistID); err != nil {
		return err
	}

	now := time.Now().Unix()
	seen := make(map[int64]bool, len(refs))
	pos := 0
	for _, ref := range refs {
		id, err := refTrackIDInLibrary(tx, libraryID, ref)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.Exec(
			`INSERT INTO playlist_entry (playlist_id, position, track_id, added_at)
			 VALUES (?, ?, ?, ?)`,
			playlistID, pos, id, now,
		); err != nil {
			return err
		}
		pos++
	}
	if _, err := tx.Exec(`UPDATE playlist SET updated_at = ? WHERE id = ?`, now, playlistID); err != nil {
		return err
	}
	return tx.Commit()
}

// PruneRemotePlaylists deletes mirrored playlists in libraryID whose remote_id
// is absent from keepRemoteIDs.
func (s *Store) PruneRemotePlaylists(libraryID string, keepRemoteIDs map[string]bool) error {
	rows, err := s.db.Query(
		`SELECT id, remote_id FROM playlist WHERE library_id = ? AND remote_id <> ''`,
		libraryID,
	)
	if err != nil {
		return err
	}
	var dropIDs []string
	for rows.Next() {
		var id, remoteID string
		if err := rows.Scan(&id, &remoteID); err != nil {
			rows.Close()
			return err
		}
		if !keepRemoteIDs[remoteID] {
			dropIDs = append(dropIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range dropIDs {
		if _, err := s.db.Exec(`DELETE FROM playlist WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// SetSyncTime records when a YTM sync scope completed.
func (s *Store) SetSyncTime(libraryID, scope string, at int64) error {
	_, err := s.db.Exec(
		`INSERT INTO ytm_sync (library_id, scope, synced_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(library_id, scope) DO UPDATE SET synced_at = excluded.synced_at`,
		libraryID, scope, at,
	)
	return err
}

// SyncTime returns the unix timestamp when scope last synced, or 0 if never.
func (s *Store) SyncTime(libraryID, scope string) (int64, error) {
	var at int64
	err := s.db.QueryRow(
		`SELECT synced_at FROM ytm_sync WHERE library_id = ? AND scope = ?`,
		libraryID, scope,
	).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return at, err
}

// AddTombstone records a deleted or unsaved remote key so a pull before YTM
// settles does not resurrect the row.
func (s *Store) AddTombstone(libraryID, key string, now int64) error {
	_, err := s.db.Exec(
		`INSERT INTO ytm_tombstone (library_id, key, deleted_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(library_id, key) DO UPDATE SET deleted_at = excluded.deleted_at`,
		libraryID, key, now,
	)
	return err
}

// ActiveTombstones returns keys deleted at or after cutoff.
func (s *Store) ActiveTombstones(libraryID string, cutoff int64) (map[string]bool, error) {
	rows, err := s.db.Query(
		`SELECT key FROM ytm_tombstone WHERE library_id = ? AND deleted_at >= ?`,
		libraryID, cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out[key] = true
	}
	return out, rows.Err()
}

// ReconcileTombstones drops tombstones that have expired before cutoff or whose
// key has already vanished from remoteKeys.
func (s *Store) ReconcileTombstones(libraryID string, remoteKeys map[string]bool, cutoff int64) error {
	rows, err := s.db.Query(
		`SELECT key, deleted_at FROM ytm_tombstone WHERE library_id = ?`,
		libraryID,
	)
	if err != nil {
		return err
	}
	var drop []string
	for rows.Next() {
		var (
			key       string
			deletedAt int64
		)
		if err := rows.Scan(&key, &deletedAt); err != nil {
			rows.Close()
			return err
		}
		if deletedAt < cutoff || !remoteKeys[key] {
			drop = append(drop, key)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, key := range drop {
		if _, err := s.db.Exec(`DELETE FROM ytm_tombstone WHERE library_id = ? AND key = ?`, libraryID, key); err != nil {
			return err
		}
	}
	return nil
}

// PlaylistHasTrack reports whether playlistID already contains ref.
func (s *Store) PlaylistHasTrack(playlistID, ref string) (bool, error) {
	var libraryID string
	if err := s.db.QueryRow(`SELECT library_id FROM playlist WHERE id = ?`, playlistID).Scan(&libraryID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	trackID, err := refTrackIDInLibrary(s.db, libraryID, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var count int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM playlist_entry WHERE playlist_id = ? AND track_id = ?`,
		playlistID, trackID,
	).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// RenamePlaylist sets a playlist's name. The library id is checked so a
// playlist from another library cannot be renamed by a stale id.
func (s *Store) RenamePlaylist(libraryID, playlistID, name string) error {
	res, err := s.db.Exec(
		`UPDATE playlist SET name = ?, updated_at = ? WHERE id = ? AND library_id = ?`,
		name, time.Now().Unix(), playlistID, libraryID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("playlist: unknown id %q", playlistID)
	}

	return nil
}

// DeletePlaylist drops a playlist. Its entries go with it through the
// ON DELETE CASCADE on playlist_entry.
func (s *Store) DeletePlaylist(libraryID, playlistID string) error {
	res, err := s.db.Exec(
		`DELETE FROM playlist WHERE id = ? AND library_id = ?`, playlistID, libraryID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("playlist: unknown id %q", playlistID)
	}

	return nil
}

// AddToPlaylist appends refs from the playlist's owning library, skipping ones
// already present.
func (s *Store) AddToPlaylist(playlistID string, refs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var libraryID string
	if err := tx.QueryRow(`SELECT library_id FROM playlist WHERE id = ?`, playlistID).Scan(&libraryID); err != nil {
		return err
	}

	var pos int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(position) + 1, 0) FROM playlist_entry WHERE playlist_id = ?`, playlistID,
	).Scan(&pos); err != nil {
		return err
	}

	for _, ref := range refs {
		id, err := refTrackIDInLibrary(tx, libraryID, ref)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM playlist_entry WHERE playlist_id = ? AND track_id = ?`,
			playlistID, id,
		).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO playlist_entry (playlist_id, position, track_id, added_at)
			 VALUES (?, ?, ?, ?)`, playlistID, pos, id, time.Now().Unix(),
		); err != nil {
			return err
		}
		pos++
	}
	if _, err := tx.Exec(`UPDATE playlist SET updated_at = ? WHERE id = ?`, time.Now().Unix(), playlistID); err != nil {
		return err
	}

	return tx.Commit()
}

// PrependToPlaylist inserts refs at position 0 in order, moving any matching
// existing entries to the front while preserving relative order of remaining tracks.
func (s *Store) PrependToPlaylist(playlistID string, refs []string) error {
	if len(refs) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var libraryID string
	if err := tx.QueryRow(`SELECT library_id FROM playlist WHERE id = ?`, playlistID).Scan(&libraryID); err != nil {
		return err
	}

	seen := make(map[int64]bool, len(refs))
	headIDs := make([]int64, 0, len(refs))
	for _, ref := range refs {
		id, err := refTrackIDInLibrary(tx, libraryID, ref)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		headIDs = append(headIDs, id)
	}
	if len(headIDs) == 0 {
		return nil
	}

	rows, err := tx.Query(
		`SELECT track_id, added_at FROM playlist_entry WHERE playlist_id = ? ORDER BY position ASC`,
		playlistID,
	)
	if err != nil {
		return err
	}
	type existingEntry struct {
		trackID int64
		addedAt int64
	}
	var tail []existingEntry
	for rows.Next() {
		var e existingEntry
		if err := rows.Scan(&e.trackID, &e.addedAt); err != nil {
			_ = rows.Close()
			return err
		}
		if !seen[e.trackID] {
			tail = append(tail, e)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	if _, err := tx.Exec(`DELETE FROM playlist_entry WHERE playlist_id = ?`, playlistID); err != nil {
		return err
	}

	now := time.Now().Unix()
	pos := 0
	for _, id := range headIDs {
		if _, err := tx.Exec(
			`INSERT INTO playlist_entry (playlist_id, position, track_id, added_at)
			 VALUES (?, ?, ?, ?)`, playlistID, pos, id, now,
		); err != nil {
			return err
		}
		pos++
	}
	for _, e := range tail {
		if _, err := tx.Exec(
			`INSERT INTO playlist_entry (playlist_id, position, track_id, added_at)
			 VALUES (?, ?, ?, ?)`, playlistID, pos, e.trackID, e.addedAt,
		); err != nil {
			return err
		}
		pos++
	}

	if _, err := tx.Exec(`UPDATE playlist SET updated_at = ? WHERE id = ?`, now, playlistID); err != nil {
		return err
	}

	return tx.Commit()
}

// RemoveFromPlaylist deletes refs belonging to the playlist's owning library
// and closes the position gaps.
func (s *Store) RemoveFromPlaylist(playlistID string, refs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var libraryID string
	if err := tx.QueryRow(`SELECT library_id FROM playlist WHERE id = ?`, playlistID).Scan(&libraryID); err != nil {
		return err
	}

	for _, ref := range refs {
		id, err := refTrackIDInLibrary(tx, libraryID, ref)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}

			return err
		}
		if _, err := tx.Exec(
			`DELETE FROM playlist_entry WHERE playlist_id = ? AND track_id = ?`, playlistID, id,
		); err != nil {
			return err
		}
	}

	// Positions are dense, so a delete leaves a hole. Renumber in Go rather
	// than with a correlated UPDATE: SQLite may rewrite rows one at a time, and
	// a subquery over the table being updated would then read half-new values.
	rows, err := tx.Query(
		`SELECT track_id FROM playlist_entry WHERE playlist_id = ? ORDER BY position`, playlistID,
	)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()

			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for i, id := range ids {
		if _, err := tx.Exec(
			`UPDATE playlist_entry SET position = ? WHERE playlist_id = ? AND track_id = ?`,
			i, playlistID, id,
		); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE playlist SET updated_at = ? WHERE id = ?`, time.Now().Unix(), playlistID); err != nil {
		return err
	}

	return tx.Commit()
}

// ReferencedCoverIDs returns every non-empty cover_id referenced by tracks,
// libraries, or playlists so artwork cache pruning never removes an active or
// composite cover.
func (s *Store) ReferencedCoverIDs() (map[string]struct{}, error) {
	rows, err := s.db.Query(`
		SELECT cover_id FROM track WHERE cover_id <> ''
		UNION
		SELECT cover_id FROM library WHERE cover_id <> ''
		UNION
		SELECT cover_id FROM playlist WHERE cover_id <> ''
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			out[id] = struct{}{}
		}
	}

	return out, rows.Err()
}

func (s *Store) count(query string, args ...any) (int, error) {
	var n int
	err := s.db.QueryRow(query, args...).Scan(&n)

	return n, err
}

func collectTracks(rows *sql.Rows, total int) ([]library.Track, int, error) {
	var out []library.Track
	for rows.Next() {
		t, err := scanTrack(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}

	return out, total, rows.Err()
}

func albumID(libraryID, album, artist string) string {
	return libraryID + "\x00" + album + "\x00" + artist
}

// ftsQuery quotes each word so a query is data, not FTS syntax. Prefix matching
// is kept because a search is normally typed left to right.
func ftsQuery(q string) string {
	fields := strings.Fields(q)
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ReplaceAll(f, `"`, `""`)
		parts = append(parts, `"`+f+`"*`)
	}

	return strings.Join(parts, " AND ")
}
