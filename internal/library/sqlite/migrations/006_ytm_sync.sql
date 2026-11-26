-- Version 6: track per-library YTM sync timestamps and speed up remote playlist lookups.
CREATE TABLE IF NOT EXISTS ytm_sync (
	library_id TEXT NOT NULL REFERENCES library(id) ON DELETE CASCADE,
	scope      TEXT NOT NULL,
	synced_at  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (library_id, scope)
);

CREATE INDEX IF NOT EXISTS playlist_library_remote ON playlist(library_id, remote_id);
