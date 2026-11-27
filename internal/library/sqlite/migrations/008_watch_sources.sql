-- Version 8: per-folder and per-playlist watch toggles for local libraries.
ALTER TABLE library_root ADD COLUMN watched INTEGER NOT NULL DEFAULT 1;
ALTER TABLE playlist ADD COLUMN source_path TEXT NOT NULL DEFAULT '';
ALTER TABLE playlist ADD COLUMN watched INTEGER NOT NULL DEFAULT 0;

UPDATE playlist
SET watched = 1
WHERE remote_id <> ''
  AND library_id IN (SELECT id FROM library WHERE kind = 'local');
