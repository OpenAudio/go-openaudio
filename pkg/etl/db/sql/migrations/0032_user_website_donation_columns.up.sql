-- The website and donation profile fields are written by the User Create/Update
-- handlers but were never part of the ETL schema (they exist in the inherited
-- production DB from the legacy indexer, but not in a fresh ETL-migrated DB).
-- Add them so user writes don't fail on fresh deployments. IF NOT EXISTS makes
-- this a no-op where the columns already exist (prod).

ALTER TABLE users ADD COLUMN IF NOT EXISTS website character varying;
ALTER TABLE users ADD COLUMN IF NOT EXISTS donation character varying;
