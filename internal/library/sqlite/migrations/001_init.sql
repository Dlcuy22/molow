-- Version 1 of the library store. Every row here is either rebuildable from
-- the filesystem or backed by the JSON settings file; the irreplaceable part is
-- the like and playlist rows, which share this database on purpose.

CREATE TABLE IF NOT EXISTS library (
	id           TEXT PRIMARY KEY,
	kind         TEXT NOT NULL,
	name         TEXT NOT NULL,
	root         TEXT NOT NULL DEFAULT '',
	account_id   TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	last_scan_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS track (
	id           INTEGER PRIMARY KEY,
	library_id   TEXT NOT NULL REFERENCES library(id) ON DELETE CASCADE,
	native_id    TEXT NOT NULL,
	title        TEXT NOT NULL DEFAULT '',
	artist       TEXT NOT NULL DEFAULT '',
	album        TEXT NOT NULL DEFAULT '',
	album_artist TEXT NOT NULL DEFAULT '',
	genre        TEXT NOT NULL DEFAULT '',
	year         INTEGER NOT NULL DEFAULT 0,
	track_no     INTEGER NOT NULL DEFAULT 0,
	disc_no      INTEGER NOT NULL DEFAULT 0,
	duration_ms  INTEGER NOT NULL DEFAULT 0,
	cover_id     TEXT NOT NULL DEFAULT '',
	added_at     INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL DEFAULT 0,
	UNIQUE (library_id, native_id)
);

CREATE TABLE IF NOT EXISTS track_file (
	path     TEXT PRIMARY KEY,
	track_id INTEGER NOT NULL REFERENCES track(id) ON DELETE CASCADE,
	dev      INTEGER NOT NULL DEFAULT 0,
	ino      INTEGER NOT NULL DEFAULT 0,
	size     INTEGER NOT NULL DEFAULT 0,
	mtime    INTEGER NOT NULL DEFAULT 0,
	seen_at  INTEGER NOT NULL DEFAULT 0,
	missing  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS track_file_track ON track_file(track_id);
CREATE INDEX IF NOT EXISTS track_file_identity ON track_file(dev, ino);
CREATE INDEX IF NOT EXISTS track_library_artist ON track(library_id, artist);
CREATE INDEX IF NOT EXISTS track_library_album ON track(library_id, album);

CREATE TABLE IF NOT EXISTS like (
	track_id   INTEGER PRIMARY KEY REFERENCES track(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS playlist (
	id          TEXT PRIMARY KEY,
	library_id  TEXT NOT NULL REFERENCES library(id) ON DELETE CASCADE,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	remote_id   TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS playlist_entry (
	playlist_id TEXT NOT NULL REFERENCES playlist(id) ON DELETE CASCADE,
	position    INTEGER NOT NULL,
	track_id    INTEGER NOT NULL REFERENCES track(id) ON DELETE CASCADE,
	added_at    INTEGER NOT NULL,
	PRIMARY KEY (playlist_id, position)
);

CREATE TABLE IF NOT EXISTS play_event (
	id         INTEGER PRIMARY KEY,
	track_id   INTEGER NOT NULL REFERENCES track(id) ON DELETE CASCADE,
	started_at INTEGER NOT NULL,
	played_ms  INTEGER NOT NULL DEFAULT 0,
	completed  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS play_event_started ON play_event(started_at);

CREATE VIRTUAL TABLE IF NOT EXISTS track_fts USING fts5(
	title, artist, album,
	content = 'track', content_rowid = 'id',
	tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS track_ai AFTER INSERT ON track BEGIN
	INSERT INTO track_fts(rowid, title, artist, album)
	VALUES (new.id, new.title, new.artist, new.album);
END;

CREATE TRIGGER IF NOT EXISTS track_ad AFTER DELETE ON track BEGIN
	INSERT INTO track_fts(track_fts, rowid, title, artist, album)
	VALUES ('delete', old.id, old.title, old.artist, old.album);
END;

CREATE TRIGGER IF NOT EXISTS track_au AFTER UPDATE ON track BEGIN
	INSERT INTO track_fts(track_fts, rowid, title, artist, album)
	VALUES ('delete', old.id, old.title, old.artist, old.album);
	INSERT INTO track_fts(rowid, title, artist, album)
	VALUES (new.id, new.title, new.artist, new.album);
END;
