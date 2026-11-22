-- Version 3: pre-rendered composite or custom cover artwork on libraries and
-- playlists. When custom_cover = 1, automatic scans and playlist edits leave
-- cover_id untouched so user-selected art is never overwritten.

ALTER TABLE library ADD COLUMN cover_id TEXT NOT NULL DEFAULT '';
ALTER TABLE library ADD COLUMN custom_cover INTEGER NOT NULL DEFAULT 0;
ALTER TABLE playlist ADD COLUMN cover_id TEXT NOT NULL DEFAULT '';
ALTER TABLE playlist ADD COLUMN custom_cover INTEGER NOT NULL DEFAULT 0;
