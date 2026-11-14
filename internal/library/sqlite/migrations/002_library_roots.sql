-- Version 2: a library can watch several folders, and a track row can name the
-- origin it came from (a local file or a remote reference), so both share one
-- catalogue. Both steps are additive: existing rows keep working unchanged.

CREATE TABLE IF NOT EXISTS library_root (
	library_id TEXT NOT NULL REFERENCES library(id) ON DELETE CASCADE,
	path       TEXT NOT NULL,
	added_at   INTEGER NOT NULL,
	PRIMARY KEY (library_id, path)
);

-- Every existing single-root library becomes a one-root library.
INSERT INTO library_root (library_id, path, added_at)
SELECT id, root, created_at FROM library WHERE root <> '';

ALTER TABLE track ADD COLUMN kind TEXT NOT NULL DEFAULT 'local';
