-- Irreversible by design. The up migration discards the only copy of each
-- dangling artist_pick_track_id, and the pre-0039 values cannot be recovered
-- from the users table. They remain recoverable from the chain (the User
-- Update tx that set each pick) if a restore is ever genuinely needed.
--
-- Nothing to undo: a NULL artist_pick_track_id is valid under the old schema,
-- and the pre-0039 writer treats these rows exactly as it did before.
SELECT 1;
