-- Version 5: virtual libraries with optional descriptions, per-library file
-- ownership so two libraries can import the same folder independently, and
-- global ref-keyed likes that stay consistent across libraries.

ALTER TABLE library ADD COLUMN description TEXT NOT NULL DEFAULT '';

CREATE TABLE track_file_new (
	library_id TEXT NOT NULL REFERENCES library(id) ON DELETE CASCADE,
	path       TEXT NOT NULL,
	track_id   INTEGER NOT NULL REFERENCES track(id) ON DELETE CASCADE,
	dev        INTEGER NOT NULL DEFAULT 0,
	ino        INTEGER NOT NULL DEFAULT 0,
	size       INTEGER NOT NULL DEFAULT 0,
	mtime      INTEGER NOT NULL DEFAULT 0,
	seen_at    INTEGER NOT NULL DEFAULT 0,
	missing    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (library_id, path)
);

INSERT INTO track_file_new (library_id, path, track_id, dev, ino, size, mtime, seen_at, missing)
SELECT t.library_id, f.path, f.track_id, f.dev, f.ino, f.size, f.mtime, f.seen_at, f.missing
FROM track_file f
JOIN track t ON t.id = f.track_id;

DROP TABLE track_file;
ALTER TABLE track_file_new RENAME TO track_file;

CREATE INDEX IF NOT EXISTS track_file_track ON track_file(track_id);
CREATE INDEX IF NOT EXISTS track_file_identity ON track_file(library_id, dev, ino);

CREATE TABLE like_new (
	ref        TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL
);

INSERT OR IGNORE INTO like_new (ref, created_at)
SELECT CASE t.kind
         WHEN 'local' THEN COALESCE((SELECT f.path FROM track_file f WHERE f.track_id = t.id ORDER BY f.path LIMIT 1), '')
         WHEN 'ytm' THEN 'ytm:' || t.native_id
         ELSE ''
       END AS ref,
       l.created_at
FROM like l
JOIN track t ON t.id = l.track_id
WHERE CASE t.kind
        WHEN 'local' THEN COALESCE((SELECT f.path FROM track_file f WHERE f.track_id = t.id ORDER BY f.path LIMIT 1), '')
        WHEN 'ytm' THEN 'ytm:' || t.native_id
        ELSE ''
      END <> '';

DROP TABLE like;
ALTER TABLE like_new RENAME TO like;
