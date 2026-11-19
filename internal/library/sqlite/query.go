package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dlcuy22/molow/internal/library"
)

// refSQL derives a row's playable ref. A local track is played by its file
// path, which track_file owns so a moved file resolves to its new path without
// touching a stored ref; a missing file has no ref, which the caller treats as
// unplayable. A remote row is played by its kind and the provider's own id,
// which is exactly library.RemoteRef, and the two kinds are listed explicitly
// so a new one has to be taught to remoteID as well: a ref this query invents
// but the resolver cannot read would fail at play time instead of at review.
const refSQL = `CASE t.kind
	WHEN 'local' THEN COALESCE((SELECT f.path FROM track_file f
	                            WHERE f.track_id = t.id AND f.missing = 0
	                            ORDER BY f.path LIMIT 1), '')
	WHEN 'ytm' THEN 'ytm:' || t.native_id
	ELSE ''
END`

// trackColumns is the one projection every track query uses.
const trackColumns = `
	t.library_id, t.kind, t.native_id, t.title, t.artist, t.album, t.album_artist,
	t.genre, t.year, t.track_no, t.disc_no, t.duration_ms, t.cover_id,
	` + refSQL + ` AS ref,
	(l.track_id IS NOT NULL) AS liked`

const trackFrom = ` FROM track t LEFT JOIN like l ON l.track_id = t.id`

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
			          t.disc_no, t.track_no, t.title COLLATE NOCASE
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

// AlbumTracks returns one album in track order.
func (s *Store) AlbumTracks(libraryID, album, artist string) ([]library.Track, error) {
	rows, err := s.db.Query(
		`SELECT`+trackColumns+trackFrom+
			` WHERE t.library_id = ? AND t.album = ?
			   AND COALESCE(NULLIF(t.album_artist, ''), t.artist) = ?
			 ORDER BY t.disc_no, t.track_no, t.title COLLATE NOCASE`,
		libraryID, album, artist,
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
			  JOIN track_fts ON track_fts.rowid = t.id
			  LEFT JOIN like l ON l.track_id = t.id
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

// remoteID reports the provider id when ref is a remote reference, which is
// how a ref names a row that has no file. A local ref is a path, and a Windows
// path carries a colon of its own, so the scheme is matched by its prefix
// rather than by splitting on the first colon.
func remoteID(ref string) (library.Kind, string, bool) {
	kind, id, ok := strings.Cut(ref, ":")
	if !ok || kind != string(library.KindYTM) || id == "" {
		return "", "", false
	}

	return library.KindYTM, id, true
}

// rowQuerier is the read surface both a store and a transaction expose, so a
// ref resolves the same way inside and outside a transaction.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// refTrackID resolves a playable ref to its catalogue row. A local ref is a
// file path; a remote ref is a kind and a native id, because a remote row has
// no track_file. The engine only ever sees the ref, so when two libraries hold
// the same provider id the earliest row is the one the ref means; ordering it
// explicitly keeps TrackByRef and this lookup on the same row.
func refTrackID(q rowQuerier, ref string) (int64, error) {
	var id int64
	if kind, native, ok := remoteID(ref); ok {
		err := q.QueryRow(
			`SELECT id FROM track WHERE kind = ? AND native_id = ? ORDER BY id LIMIT 1`,
			string(kind), native,
		).Scan(&id)

		return id, err
	}
	err := q.QueryRow(`SELECT track_id FROM track_file WHERE path = ?`, ref).Scan(&id)

	return id, err
}

// TrackByRef resolves a playable ref back to its catalogue row.
func (s *Store) TrackByRef(ref string) (library.Track, bool, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if kind, native, ok := remoteID(ref); ok {
		rows, err = s.db.Query(
			`SELECT`+trackColumns+trackFrom+
				` WHERE t.kind = ? AND t.native_id = ? ORDER BY t.id LIMIT 1`,
			string(kind), native,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT`+trackColumns+
				` FROM track t
				  JOIN track_file f ON f.track_id = t.id AND f.path = ?
				  LEFT JOIN like l ON l.track_id = t.id
				  LIMIT 1`, ref,
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

// SetLiked inserts or removes a like for a ref.
func (s *Store) SetLiked(ref string, liked bool) error {
	_, ok, err := s.TrackByRef(ref)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("library: unknown ref %q", ref)
	}
	id, err := refTrackID(s.db, ref)
	if err != nil {
		return err
	}
	if liked {
		_, err = s.db.Exec(
			`INSERT INTO like (track_id, created_at) VALUES (?, ?)
			 ON CONFLICT(track_id) DO NOTHING`, id, time.Now().Unix(),
		)

		return err
	}
	_, err = s.db.Exec(`DELETE FROM like WHERE track_id = ?`, id)

	return err
}

// Liked reports whether a ref carries a like.
func (s *Store) Liked(ref string) (bool, error) {
	id, err := refTrackID(s.db, ref)
	if err != nil {
		return false, err
	}
	var n int
	err = s.db.QueryRow(`SELECT COUNT(*) FROM like WHERE track_id = ?`, id).Scan(&n)

	return n > 0, err
}

// ToggleLiked flips one like and returns the state it now has.
func (s *Store) ToggleLiked(ref string) (bool, error) {
	id, err := refTrackID(s.db, ref)
	if err != nil {
		return false, err
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM like WHERE track_id = ?`, id).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		_, err = s.db.Exec(`DELETE FROM like WHERE track_id = ?`, id)

		return false, err
	}
	_, err = s.db.Exec(`INSERT INTO like (track_id, created_at) VALUES (?, ?)`, id, time.Now().Unix())

	return true, err
}

// Playlists lists a library's playlists with their entry counts.
func (s *Store) Playlists(libraryID string) ([]library.Playlist, error) {
	rows, err := s.db.Query(
		`SELECT p.id, p.name, p.description, p.remote_id,
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
		var p library.Playlist
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.RemoteID, &p.TrackCount); err != nil {
			return nil, err
		}
		p.LibraryID = libraryID
		out = append(out, p)
	}

	return out, rows.Err()
}

// PlaylistTracks reads a playlist in position order.
func (s *Store) PlaylistTracks(playlistID string, limit, offset int) ([]library.Track, error) {
	rows, err := s.db.Query(
		`SELECT`+trackColumns+
			` FROM playlist_entry e
			  JOIN track t ON t.id = e.track_id
			  LEFT JOIN like l ON l.track_id = t.id
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

// CreatePlaylist adds one and returns it.
func (s *Store) CreatePlaylist(libraryID, name string) (library.Playlist, error) {
	return s.CreatePlaylistWithRemote(libraryID, name, "")
}

// CreatePlaylistWithRemote adds a playlist that mirrors a remote list, so the
// row remembers which list it came from. Its entries are added afterwards.
func (s *Store) CreatePlaylistWithRemote(libraryID, name, remoteID string) (library.Playlist, error) {
	// The id is namespaced by time, not by the remote id: the same remote list
	// can be imported twice and both rows are honest.
	id := fmt.Sprintf("playlist:%d", time.Now().UnixNano())
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`INSERT INTO playlist (id, library_id, name, remote_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`, id, libraryID, name, remoteID, now, now,
	)
	if err != nil {
		return library.Playlist{}, err
	}

	return library.Playlist{ID: id, LibraryID: libraryID, Name: name, RemoteID: remoteID}, nil
}

// AddToPlaylist appends refs, skipping ones already present.
func (s *Store) AddToPlaylist(playlistID string, refs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var pos int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(position) + 1, 0) FROM playlist_entry WHERE playlist_id = ?`, playlistID,
	).Scan(&pos); err != nil {
		return err
	}

	for _, ref := range refs {
		id, err := refTrackID(tx, ref)
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

// RemoveFromPlaylist deletes refs and closes the position gaps.
func (s *Store) RemoveFromPlaylist(playlistID string, refs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, ref := range refs {
		id, err := refTrackID(tx, ref)
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
