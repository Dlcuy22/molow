// Package sqlite is the durable half of the library. One database holds the
// registry, the catalogue, likes, playlists and play history; a file tree and
// an account are both rebuildable, but a like is not, so they share a store.
package sqlite

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/dlcuy22/molow/internal/library"
	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store is the one writer for the database. SQLite serialises writers anyway;
// keeping one connection makes that explicit and removes SQLITE_BUSY from the
// call sites.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the database at path. A missing parent directory is
// created, because the first launch has no config directory yet.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("library: create config dir: %w", err)
	}

	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(wal)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(normal)" +
		"&_txlock=immediate"

	db, err := driver.Open(dsn, func(c *sqlite3.Conn) error {
		return fts5.Register(c)
	})
	if err != nil {
		return nil, fmt.Errorf("library: open: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()

		return nil, err
	}

	return s, nil
}

// OpenMemory is the test entry point. It applies the same migrations, so a
// query test cannot pass on a schema production does not have.
func OpenMemory() (*Store, error) {
	db, err := driver.Open(":memory:", func(c *sqlite3.Conn) error {
		return fts5.Register(c)
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()

		return nil, err
	}

	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate applies every embedded migration newer than PRAGMA user_version, in
// order, one transaction each. An interrupted migration leaves the version
// where it was, so the next open retries the same step.
func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("library: read schema version: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for i, name := range names {
		next := i + 1
		if next <= version {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()

			return fmt.Errorf("library: migration %s: %w", name, err)
		}
		// PRAGMA does not accept a bind parameter; the value is an integer we
		// produced, never input.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", next)); err != nil {
			_ = tx.Rollback()

			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	return nil
}

// AddLibrary registers a library and returns its row.
func (s *Store) AddLibrary(lib library.Library) (library.Library, error) {
	if lib.ID == "" {
		return library.Library{}, errors.New("library: empty id")
	}
	now := time.Now().Unix()
	accountID := lib.Root
	if lib.Kind == library.KindYTM && accountID == "" {
		accountID = strings.TrimPrefix(lib.ID, "ytm:")
	}
	_, err := s.db.Exec(
		`INSERT INTO library (id, kind, name, description, root, account_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description, root = excluded.root, account_id = excluded.account_id`,
		lib.ID, string(lib.Kind), lib.Name, lib.Description, lib.Root, accountID, now,
	)
	if err != nil {
		return library.Library{}, err
	}

	return s.Library(lib.ID)
}

// UpdateLibrary changes a library's name and description and returns the updated row.
func (s *Store) UpdateLibrary(id, name, description string) (library.Library, error) {
	res, err := s.db.Exec(
		`UPDATE library SET name = ?, description = ? WHERE id = ?`,
		name, description, id,
	)
	if err != nil {
		return library.Library{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return library.Library{}, err
	}
	if n == 0 {
		return library.Library{}, fmt.Errorf("library: unknown id %q", id)
	}

	return s.Library(id)
}

// AddRoot registers one more watched folder. It reports whether the root is
// new: re-adding a folder the library already watches is a no-op, not an error.
func (s *Store) AddRoot(libraryID, path string) (bool, error) {
	res, err := s.db.Exec(
		`INSERT INTO library_root (library_id, path, added_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(library_id, path) DO NOTHING`,
		libraryID, path, time.Now().Unix(),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()

	return n > 0, err
}

// Roots lists a library's watched folders in insertion order, which is the
// chronological one: two roots added in the same second would tie on a
// timestamp.
func (s *Store) Roots(libraryID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT path FROM library_root WHERE library_id = ? ORDER BY rowid`, libraryID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}

	return out, rows.Err()
}

// WatchedRoots lists a library's watched folders with watching enabled, in insertion order.
func (s *Store) WatchedRoots(libraryID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT path FROM library_root WHERE library_id = ? AND watched = 1 ORDER BY rowid`, libraryID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}

	return out, rows.Err()
}

// SetRootWatched enables or disables live watching and scanning for path in libraryID.
func (s *Store) SetRootWatched(libraryID, path string, watched bool) error {
	w := 0
	if watched {
		w = 1
	}
	_, err := s.db.Exec(
		`UPDATE library_root SET watched = ? WHERE library_id = ? AND path = ?`,
		w, libraryID, path,
	)
	return err
}

// HasWatchedSources reports whether libraryID has at least one watched folder
// or watched imported remote playlist.
func (s *Store) HasWatchedSources(libraryID string) (bool, error) {
	var n int
	if err := s.db.QueryRow(
		`SELECT EXISTS(
			SELECT 1 FROM library_root WHERE library_id = ? AND watched = 1
			UNION ALL
			SELECT 1 FROM playlist WHERE library_id = ? AND remote_id <> '' AND watched = 1
		)`,
		libraryID, libraryID,
	).Scan(&n); err != nil {
		return false, err
	}
	return n != 0, nil
}

// HasWatchedRemotePlaylists reports whether libraryID has any watched remote playlist.
func (s *Store) HasWatchedRemotePlaylists(libraryID string) (bool, error) {
	var n int
	if err := s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM playlist WHERE library_id = ? AND remote_id <> '' AND watched = 1)`,
		libraryID,
	).Scan(&n); err != nil {
		return false, err
	}
	return n != 0, nil
}

// LibraryLogoCovers returns up to n distinct cover ids, the most recently
// added first (ties by id, so the order is total). A library's albums share
// one cover, so the distinct keeps one album from filling the sidebar card's
// whole 2x2 grid.
func (s *Store) LibraryLogoCovers(libraryID string, n int) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT cover_id FROM track
		 WHERE library_id = ? AND cover_id <> ''
		 GROUP BY cover_id
		 ORDER BY MAX(added_at) DESC, cover_id
		 LIMIT ?`, libraryID, n,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}

	return out, rows.Err()
}

// Library reads one row with its track count.
func (s *Store) Library(id string) (library.Library, error) {
	var (
		out         library.Library
		kind        string
		customCover int
	)
	err := s.db.QueryRow(
		`SELECT l.id, l.kind, l.name, l.description, l.root, l.last_scan_at, l.cover_id, l.custom_cover,
		        (SELECT COUNT(*) FROM track t WHERE t.library_id = l.id)
		 FROM library l WHERE l.id = ?`, id,
	).Scan(&out.ID, &kind, &out.Name, &out.Description, &out.Root, new(int64), &out.CoverID, &customCover, &out.TrackCount)
	if err != nil {
		return library.Library{}, err
	}
	out.Kind = library.Kind(kind)
	out.Status = library.StatusReady
	out.CustomCover = customCover != 0
	roots, err := s.Roots(id)
	if err != nil {
		return library.Library{}, err
	}
	out.Roots = roots
	covers, err := s.LibraryLogoCovers(id, 4)
	if err != nil {
		return library.Library{}, err
	}
	out.LogoCovers = covers

	return out, nil
}

// Libraries lists every registered library.
func (s *Store) Libraries() ([]library.Library, error) {
	rows, err := s.db.Query(
		`SELECT l.id, l.kind, l.name, l.description, l.root, l.cover_id, l.custom_cover,
		        (SELECT COUNT(*) FROM track t WHERE t.library_id = l.id)
		 FROM library l ORDER BY l.created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []library.Library
	for rows.Next() {
		var (
			l           library.Library
			kind        string
			customCover int
		)
		if err := rows.Scan(&l.ID, &kind, &l.Name, &l.Description, &l.Root, &l.CoverID, &customCover, &l.TrackCount); err != nil {
			return nil, err
		}
		l.Kind = library.Kind(kind)
		l.Status = library.StatusReady
		l.CustomCover = customCover != 0
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Close the outer cursor before running follow-up queries on the single SQLite connection.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		roots, err := s.Roots(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Roots = roots
		covers, err := s.LibraryLogoCovers(out[i].ID, 4)
		if err != nil {
			return nil, err
		}
		out[i].LogoCovers = covers
	}

	return out, nil
}

// SetLibraryCover updates a library's stored cover_id. When custom is false,
// rows with custom_cover = 1 are left untouched so automatic scans never
// overwrite user-selected artwork.
func (s *Store) SetLibraryCover(libraryID, coverID string, custom bool) error {
	if custom {
		_, err := s.db.Exec(
			`UPDATE library SET cover_id = ?, custom_cover = 1 WHERE id = ?`,
			coverID, libraryID,
		)

		return err
	}
	_, err := s.db.Exec(
		`UPDATE library SET cover_id = ? WHERE id = ? AND custom_cover = 0`,
		coverID, libraryID,
	)

	return err
}

// RemoveLibrary deletes a row and, by cascade, its catalogue.
func (s *Store) RemoveLibrary(id string) error {
	_, err := s.db.Exec(`DELETE FROM library WHERE id = ?`, id)

	return err
}

// File is the identity of one path on disk.
type File struct {
	Path  string
	Dev   uint64
	Ino   uint64
	Size  int64
	Mtime int64
}

// TrackRow is the catalogue row as the scanner writes it.
type TrackRow struct {
	NativeID     string
	Title        string
	Artist       string
	Album        string
	AlbumArtist  string
	Genre        string
	Year         int
	TrackNo      int
	DiscNo       int
	DurationMs   int64
	CoverID      string
	ThumbnailURL string
	ArtistID     string
}

// RemoteMeta holds the provider-facing URLs and IDs persisted alongside a
// remote track so presence and art lookups survive restarts without a re-query.
type RemoteMeta struct {
	ThumbnailURL string
	ArtistID     string
}

// TouchIfUnchanged checks whether f.Path is indexed under libraryID with
// matching size and mtime. When unchanged, it updates seen_at and clears
// missing without requiring tag metadata, returning (trackID, true, nil).
//
// It returns (0, false, nil) when the path is unknown in libraryID, changed,
// missing its cached cover according to hasCover, or missing a track or disc
// index that fallbackTrack or fallbackDisc can supply.
func (s *Store) TouchIfUnchanged(
	libraryID string,
	f File,
	fallbackTrack, fallbackDisc int,
	hasCover func(coverID string) bool,
) (int64, bool, error) {
	var (
		trackID    int64
		oldSize    int64
		oldMtime   int64
		coverID    string
		oldTrackNo int
		oldDiscNo  int
	)
	err := s.db.QueryRow(
		`SELECT f.track_id, f.size, f.mtime, t.cover_id, t.track_no, t.disc_no
		   FROM track_file f JOIN track t ON t.id = f.track_id
		  WHERE f.library_id = ? AND f.path = ?`, libraryID, f.Path,
	).Scan(&trackID, &oldSize, &oldMtime, &coverID, &oldTrackNo, &oldDiscNo)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if oldSize != f.Size || oldMtime != f.Mtime {
		return 0, false, nil
	}
	if coverID != "" && hasCover != nil && !hasCover(coverID) {
		return 0, false, nil
	}
	if (oldTrackNo == 0 && fallbackTrack > 0) || (oldDiscNo == 0 && fallbackDisc > 0) {
		return 0, false, nil
	}

	if _, err := s.db.Exec(
		`UPDATE track_file SET seen_at = ?, missing = 0 WHERE library_id = ? AND path = ?`,
		time.Now().Unix(), libraryID, f.Path,
	); err != nil {
		return 0, false, err
	}

	return trackID, true, nil
}

// UpsertTrack writes one file into libraryID's catalogue.
//
// Size and mtime matching means unchanged; a known (dev, ino) at a new path
// within the same library is a move and keeps the track id and global like; a
// new path with a new identity is a new track.
func (s *Store) UpsertTrack(libraryID string, f File, t TrackRow) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	var (
		trackID    int64
		oldSize    int64
		oldMtime   int64
		oldTrackNo int
		oldDiscNo  int
	)
	err = tx.QueryRow(
		`SELECT f.track_id, f.size, f.mtime, t.track_no, t.disc_no
		   FROM track_file f JOIN track t ON t.id = f.track_id
		  WHERE f.library_id = ? AND f.path = ?`, libraryID, f.Path,
	).Scan(&trackID, &oldSize, &oldMtime, &oldTrackNo, &oldDiscNo)
	switch {
	case err == nil && oldSize == f.Size && oldMtime == f.Mtime:
		// Backfill missing track/disc indices on rescan without rewriting tags.
		if (oldTrackNo == 0 && t.TrackNo > 0) || (oldDiscNo == 0 && t.DiscNo > 0) {
			nextTrack := oldTrackNo
			if nextTrack == 0 {
				nextTrack = t.TrackNo
			}
			nextDisc := oldDiscNo
			if nextDisc == 0 {
				nextDisc = t.DiscNo
			}
			if _, err := tx.Exec(
				`UPDATE track SET track_no = ?, disc_no = ?, updated_at = ? WHERE id = ?`,
				nextTrack, nextDisc, now, trackID,
			); err != nil {
				return 0, err
			}
		}
		_, err = tx.Exec(
			`UPDATE track_file SET seen_at = ?, missing = 0 WHERE library_id = ? AND path = ?`, now, libraryID, f.Path,
		)
		if err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}

		return trackID, nil
	case err == nil:
		// Same path, changed content: refresh its catalogue row in place.
		if err := updateTrack(tx, trackID, t, now); err != nil {
			return 0, err
		}
		_, err = tx.Exec(
			`UPDATE track_file SET dev=?, ino=?, size=?, mtime=?, seen_at=?, missing=0
			 WHERE library_id=? AND path=?`,
			f.Dev, f.Ino, f.Size, f.Mtime, now, libraryID, f.Path,
		)
		if err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}

		return trackID, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}

	// A move on the same filesystem is a path change on an existing track in
	// this library. Move its global like ref along with the path.
	if f.Dev != 0 && f.Ino != 0 {
		var oldPath string
		err = tx.QueryRow(
			`SELECT f.track_id, f.path FROM track_file f
			  WHERE f.library_id = ? AND f.dev = ? AND f.ino = ? LIMIT 1`,
			libraryID, f.Dev, f.Ino,
		).Scan(&trackID, &oldPath)
		if err == nil {
			if _, err := tx.Exec(
				`DELETE FROM track_file WHERE library_id = ? AND dev = ? AND ino = ? AND path <> ? AND track_id = ?`,
				libraryID, f.Dev, f.Ino, f.Path, trackID,
			); err != nil {
				return 0, err
			}
			_, err = tx.Exec(
				`INSERT INTO track_file (library_id, path, track_id, dev, ino, size, mtime, seen_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				libraryID, f.Path, trackID, f.Dev, f.Ino, f.Size, f.Mtime, now,
			)
			if err != nil {
				return 0, err
			}
			if oldPath != "" && oldPath != f.Path {
				if _, err := tx.Exec(
					`UPDATE OR IGNORE like SET ref = ? WHERE ref = ?`, f.Path, oldPath,
				); err != nil {
					return 0, err
				}
				if _, err := tx.Exec(`DELETE FROM like WHERE ref = ?`, oldPath); err != nil {
					return 0, err
				}
			}
			if err := updateTrack(tx, trackID, t, now); err != nil {
				return 0, err
			}
			if err := tx.Commit(); err != nil {
				return 0, err
			}

			return trackID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}

	res, err := tx.Exec(
		`INSERT INTO track
		 (library_id, native_id, title, artist, album, album_artist, genre,
		  year, track_no, disc_no, duration_ms, cover_id, added_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		libraryID, t.NativeID, t.Title, t.Artist, t.Album, t.AlbumArtist, t.Genre,
		t.Year, t.TrackNo, t.DiscNo, t.DurationMs, t.CoverID, now, now,
	)
	if err != nil {
		return 0, err
	}
	trackID, err = res.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(
		`INSERT INTO track_file (library_id, path, track_id, dev, ino, size, mtime, seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		libraryID, f.Path, trackID, f.Dev, f.Ino, f.Size, f.Mtime, now,
	)
	if err != nil {
		return 0, err
	}

	return trackID, tx.Commit()
}

func updateTrack(tx *sql.Tx, id int64, t TrackRow, now int64) error {
	_, err := tx.Exec(
		`UPDATE track SET title=?, artist=?, album=?, album_artist=?, genre=?,
		 year=?, track_no=?, disc_no=?, duration_ms=?, cover_id=?, updated_at=?
		 WHERE id=?`,
		t.Title, t.Artist, t.Album, t.AlbumArtist, t.Genre, t.Year, t.TrackNo,
		t.DiscNo, t.DurationMs, t.CoverID, now, id,
	)

	return err
}

// UpsertRemoteTrack writes a track that has no file behind it, such as a
// YouTube Music hit. A remote row has no track_file entry, which is what keeps
// a scan from seeing it: MarkMissing only ever touches files.
//
// The identity is (library, native id), the same uniqueness the scanner uses,
// so importing the same hit twice refreshes one row instead of duplicating it.
func (s *Store) UpsertRemoteTrack(libraryID string, kind library.Kind, t TrackRow) (int64, error) {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`INSERT INTO track
		 (library_id, kind, native_id, title, artist, album, album_artist, genre,
		  year, track_no, disc_no, duration_ms, cover_id, thumbnail_url, artist_id,
		  added_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(library_id, native_id) DO UPDATE SET
		  kind = excluded.kind, title = excluded.title, artist = excluded.artist,
		  album = COALESCE(NULLIF(excluded.album, ''), track.album),
		  album_artist = excluded.album_artist,
		  genre = excluded.genre, year = excluded.year, track_no = excluded.track_no,
		  disc_no = excluded.disc_no, duration_ms = excluded.duration_ms,
		  cover_id = COALESCE(NULLIF(excluded.cover_id, ''), track.cover_id),
		  thumbnail_url = COALESCE(NULLIF(excluded.thumbnail_url, ''), track.thumbnail_url),
		  artist_id = COALESCE(NULLIF(excluded.artist_id, ''), track.artist_id),
		  updated_at = excluded.updated_at`,
		libraryID, string(kind), t.NativeID, t.Title, t.Artist, t.Album,
		t.AlbumArtist, t.Genre, t.Year, t.TrackNo, t.DiscNo, t.DurationMs,
		t.CoverID, t.ThumbnailURL, t.ArtistID, now, now,
	)
	if err != nil {
		return 0, err
	}

	// On a conflict LastInsertId is not the updated row, so read the id back by
	// the identity the insert just wrote.
	var id int64
	err = s.db.QueryRow(
		`SELECT id FROM track WHERE library_id = ? AND native_id = ?`, libraryID, t.NativeID,
	).Scan(&id)

	return id, err
}

// TrackRemoteMeta reads the persisted remote artwork URL and artist ID for a
// remote ref, returning false when ref is not a catalogued remote track.
func (s *Store) TrackRemoteMeta(ref string) (RemoteMeta, bool, error) {
	kind, native, ok := remoteID(ref)
	if !ok {
		return RemoteMeta{}, false, nil
	}
	var out RemoteMeta
	err := s.db.QueryRow(
		`SELECT thumbnail_url, artist_id FROM track
		 WHERE kind = ? AND native_id = ? ORDER BY id LIMIT 1`,
		string(kind), native,
	).Scan(&out.ThumbnailURL, &out.ArtistID)
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteMeta{}, false, nil
	}
	if err != nil {
		return RemoteMeta{}, false, err
	}

	return out, true, nil
}

// BackfillTrackRemoteMeta fills empty thumbnail_url, artist_id, and album
// fields on catalogued rows matching ref without overwriting values already stored.
func (s *Store) BackfillTrackRemoteMeta(ref, thumbnailURL, artistID, album string) error {
	kind, native, ok := remoteID(ref)
	if !ok || (thumbnailURL == "" && artistID == "" && album == "") {
		return nil
	}
	_, err := s.db.Exec(
		`UPDATE track
		 SET thumbnail_url = CASE WHEN thumbnail_url = '' AND ? <> '' THEN ? ELSE thumbnail_url END,
		     artist_id     = CASE WHEN artist_id     = '' AND ? <> '' THEN ? ELSE artist_id END,
		     album         = CASE WHEN album         = '' AND ? <> '' THEN ? ELSE album END,
		     updated_at    = ?
		 WHERE kind = ? AND native_id = ?
		   AND ((thumbnail_url = '' AND ? <> '') OR (artist_id = '' AND ? <> '') OR (album = '' AND ? <> ''))`,
		thumbnailURL, thumbnailURL,
		artistID, artistID,
		album, album,
		time.Now().Unix(),
		string(kind), native,
		thumbnailURL, artistID, album,
	)

	return err
}

// MarkMissing flags every file row in libraryID that a scan did not see.
func (s *Store) MarkMissing(libraryID string, scanStart int64) error {
	_, err := s.db.Exec(
		`UPDATE track_file SET missing = 1 WHERE library_id = ? AND seen_at < ?`,
		libraryID, scanStart,
	)

	return err
}

// MarkMissingInRoots flags file rows under roots in libraryID that a scan did
// not see, leaving tracks in unwatched folders untouched.
func (s *Store) MarkMissingInRoots(libraryID string, roots []string, scanStart int64) error {
	if len(roots) == 0 {
		return nil
	}
	rows, err := s.db.Query(
		`SELECT path FROM track_file WHERE library_id = ? AND seen_at < ? AND missing = 0`,
		libraryID, scanStart,
	)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		for _, r := range roots {
			if pathUnderRoot(p, r) {
				stale = append(stale, p)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, p := range stale {
		if _, err := tx.Exec(
			`UPDATE track_file SET missing = 1 WHERE library_id = ? AND path = ?`,
			libraryID, p,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func pathUnderRoot(path, root string) bool {
	if root == "" {
		return false
	}
	sep := string(os.PathSeparator)
	prefix := root + sep
	if runtime.GOOS == "linux" {
		return path == root || strings.HasPrefix(path, prefix)
	}
	if strings.EqualFold(path, root) {
		return true
	}
	return len(path) > len(prefix) && strings.EqualFold(path[:len(prefix)], prefix)
}

// TouchScan records a completed scan.
func (s *Store) TouchScan(libraryID string) error {
	_, err := s.db.Exec(`UPDATE library SET last_scan_at = ? WHERE id = ?`, time.Now().Unix(), libraryID)

	return err
}
