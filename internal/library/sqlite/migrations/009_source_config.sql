-- Version 9: per-library encrypted credential reference and source-specific non-secret config JSON.
ALTER TABLE library ADD COLUMN credential_id TEXT NOT NULL DEFAULT '';
ALTER TABLE library ADD COLUMN config TEXT NOT NULL DEFAULT '';
