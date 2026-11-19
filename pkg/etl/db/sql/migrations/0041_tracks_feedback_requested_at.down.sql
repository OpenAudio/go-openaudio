DROP INDEX IF EXISTS tracks_feedback_requested_at_idx;
ALTER TABLE tracks DROP COLUMN IF EXISTS feedback_requested_at;
