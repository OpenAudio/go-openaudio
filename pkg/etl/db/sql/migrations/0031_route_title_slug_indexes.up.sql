-- Speed up slug-collision resolution on track/playlist create + rename.
-- GenerateSlugAndCollisionID runs `SELECT MAX(collision_id) ... WHERE owner_id
-- AND title_slug` on every route write, but the only index is the PK
-- (owner_id, slug) -- nothing leads with title_slug, so the MAX scans every
-- route the owner has. For prolific owners that is the slow tail (multi-second
-- Track/Playlist transactions). Adding (owner_id, title_slug, collision_id)
-- turns the MAX into an index-only lookup.
--
-- Build note: these are plain (non-CONCURRENT) builds to fit the single-
-- transaction migration runner. They take an ACCESS EXCLUSIVE lock while
-- building (track_routes ~2M rows). To avoid blocking route writes on deploy,
-- pre-build them CONCURRENTLY in prod first -- IF NOT EXISTS then makes this a
-- no-op:
--   CREATE INDEX CONCURRENTLY IF NOT EXISTS track_routes_owner_title_slug_idx
--     ON track_routes (owner_id, title_slug, collision_id);
--   CREATE INDEX CONCURRENTLY IF NOT EXISTS playlist_routes_owner_title_slug_idx
--     ON playlist_routes (owner_id, title_slug, collision_id);

CREATE INDEX IF NOT EXISTS track_routes_owner_title_slug_idx
  ON track_routes (owner_id, title_slug, collision_id);

CREATE INDEX IF NOT EXISTS playlist_routes_owner_title_slug_idx
  ON playlist_routes (owner_id, title_slug, collision_id);
