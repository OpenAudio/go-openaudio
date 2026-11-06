-- When the artist turned on "looking for feedback"; NULL when off.
ALTER TABLE tracks ADD COLUMN IF NOT EXISTS feedback_requested_at timestamp without time zone;

CREATE INDEX IF NOT EXISTS tracks_feedback_requested_at_idx
  ON tracks (feedback_requested_at DESC, track_id DESC)
  WHERE feedback_requested_at IS NOT NULL AND is_current = true AND is_delete = false;
