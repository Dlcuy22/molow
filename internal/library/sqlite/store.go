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

// AddLibrary registers a root and returns its row. A duplicate root returns the
// existing library rather than a second row.
func (s *Store) AddLibrary(lib library.Library) (library.Library, error) {
	if lib.ID == "" {
		return library.Library{}, errors.New("library: empty id")
	}
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`INSERT INTO library (id, kind, name, root, account_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, root = excluded.root`,
		lib.ID, string(lib.Kind), lib.Name, lib.Root, lib.Root, now,
	)
	if err != nil {
		return library.Library{}, err
	}

	return s.Library(lib.ID)
}

// Library reads one row with its track count.
func (s *Store) Library(id string) (library.Library, error) {
	var (
		out  library.Library
		kind string
	)
	err := s.db.QueryRow(
		`SELECT l.id, l.kind, l.name, l.root, l.last_scan_at,
		        (SELECT COUNT(*) FROM track t WHERE t.library_id = l.id)
		 FROM library l WHERE l.id = ?`, id,
	).Scan(&out.ID, &kind, &out.Name, &out.Root, new(int64), &out.TrackCount)
	if err != nil {
		return library.Library{}, err
	}
	out.Kind = library.Kind(kind)
	out.Status = library.StatusReady

	return out, nil
}

// Libraries lists every registered library.
func (s *Store) Libraries() ([]library.Library, error) {
	rows, err := s.db.Query(
		`SELECT l.id, l.kind, l.name, l.root,
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
			l    library.Library
			kind string
		)
		if err := rows.Scan(&l.ID, &kind, &l.Name, &l.Root, &l.TrackCount); err != nil {
			return nil, err
		}
		l.Kind = library.Kind(kind)
		l.Status = library.StatusReady
		out = append(out, l)
	}

	return out, rows.Err()
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
	NativeID    string
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Genre       string
	Year        int
	TrackNo     int
	DiscNo      int
	DurationMs  int64
	CoverID     string
}

// UpsertTrack writes one file into the catalogue.
//
// The rules are the plan's: size+mtime means unchanged; a known (dev, ino) at a
// new path is a move and keeps the track id, so a like survives a rename; a new
// path with a new identity is a new track.
func (s *Store) UpsertTrack(libraryID string, f File, t TrackRow) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	var (
		trackID  int64
		oldSize  int64
		oldMtime int64
	)
	err = tx.QueryRow(
		`SELECT track_id, size, mtime FROM track_file WHERE path = ?`, f.Path,
	).Scan(&trackID, &oldSize, &oldMtime)
	switch {
	case err == nil && oldSize == f.Size && oldMtime == f.Mtime:
		_, err = tx.Exec(
			`UPDATE track_file SET seen_at = ?, missing = 0 WHERE path = ?`, now, f.Path,
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
			`UPDATE track_file SET dev=?, ino=?, size=?, mtime=?, seen_at=?, missing=0 WHERE path=?`,
			f.Dev, f.Ino, f.Size, f.Mtime, now, f.Path,
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

	// A move on the same filesystem is a path change on an existing track.
	if f.Dev != 0 && f.Ino != 0 {
		err = tx.QueryRow(
			`SELECT track_id FROM track_file WHERE dev = ? AND ino = ? LIMIT 1`, f.Dev, f.Ino,
		).Scan(&trackID)
		if err == nil {
			// The old path may still be there; a move means this one now owns
			// the identity, so the old row goes.
			if _, err := tx.Exec(`DELETE FROM track_file WHERE dev = ? AND ino = ? AND path <> ?`, f.Dev, f.Ino, f.Path); err != nil {
				return 0, err
			}
			_, err = tx.Exec(
				`INSERT INTO track_file (path, track_id, dev, ino, size, mtime, seen_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				f.Path, trackID, f.Dev, f.Ino, f.Size, f.Mtime, now,
			)
			if err != nil {
				return 0, err
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
		`INSERT INTO track_file (path, track_id, dev, ino, size, mtime, seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		f.Path, trackID, f.Dev, f.Ino, f.Size, f.Mtime, now,
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

// MarkMissing flags every file row a scan did not see.
func (s *Store) MarkMissing(libraryID string, scanStart int64) error {
	_, err := s.db.Exec(
		`UPDATE track_file SET missing = 1
		 WHERE seen_at < ? AND track_id IN (SELECT id FROM track WHERE library_id = ?)`,
		scanStart, libraryID,
	)

	return err
}

// TouchScan records a completed scan.
func (s *Store) TouchScan(libraryID string) error {
	_, err := s.db.Exec(`UPDATE library SET last_scan_at = ? WHERE id = ?`, time.Now().Unix(), libraryID)

	return err
}
