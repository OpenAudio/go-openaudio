-- tag_track_user: MV exploding tracks.tags into one row per (tag, track, owner).
-- Used by tag-search queries. Ported verbatim from apps' schema.

DROP MATERIALIZED VIEW IF EXISTS tag_track_user;
CREATE MATERIALIZED VIEW tag_track_user AS
SELECT unnest(t.tags) AS tag,
       t.track_id,
       t.owner_id
FROM (
  SELECT string_to_array(lower(tracks.tags::text), ','::text) AS tags,
         tracks.track_id,
         tracks.owner_id
  FROM tracks
  WHERE tracks.tags::text <> ''
    AND tracks.tags IS NOT NULL
    AND tracks.is_current IS TRUE
    AND tracks.is_unlisted IS FALSE
    AND tracks.stem_of IS NULL
  ORDER BY tracks.updated_at DESC
) t
GROUP BY unnest(t.tags), t.track_id, t.owner_id
WITH NO DATA;

CREATE INDEX IF NOT EXISTS tag_track_user_tag_idx ON tag_track_user (tag);
CREATE INDEX IF NOT EXISTS tag_track_user_track_id_idx ON tag_track_user (track_id);
