-- playlist_seen primary key was originally declared as
-- (is_current, user_id, playlist_id, seen_at) — `is_current` was a column
-- but never a meaningful part of the uniqueness contract (the handler
-- always writes is_current=true). Prod's schema has the PK without
-- is_current. ON CONFLICT clauses that name the prod target — which is
-- the right thing to do — get rejected with 42P10 against the older
-- in-house PK.
--
-- Fresh databases skip this migration via the corrected 0005. This
-- migration converts older databases (that already ran the bad 0005) to
-- the prod-compatible shape.

ALTER TABLE playlist_seen
  DROP CONSTRAINT IF EXISTS playlist_seen_pkey;

ALTER TABLE playlist_seen
  ADD CONSTRAINT playlist_seen_pkey PRIMARY KEY (user_id, playlist_id, seen_at);
