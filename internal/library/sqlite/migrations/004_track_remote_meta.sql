-- Version 4: persist remote artwork URL and credited artist browse ID on
-- tracks so streamed entries survive restarts with direct cover/avatar links.

ALTER TABLE track ADD COLUMN thumbnail_url TEXT NOT NULL DEFAULT '';
ALTER TABLE track ADD COLUMN artist_id TEXT NOT NULL DEFAULT '';
