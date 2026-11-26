-- Version 7: track playlist ownership (owned vs saved foreign playlists) and
-- temporary deletion tombstones so pulls do not resurrect deleted playlists.
ALTER TABLE playlist ADD COLUMN owned INTEGER NOT NULL DEFAULT 1;

CREATE TABLE IF NOT EXISTS ytm_tombstone (
	library_id TEXT    NOT NULL REFERENCES library(id) ON DELETE CASCADE,
	key        TEXT    NOT NULL,
	deleted_at INTEGER NOT NULL,
	PRIMARY KEY (library_id, key)
);
