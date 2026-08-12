-- Clear artist_pick_track_id references that no longer resolve.
--
-- Until the accompanying writer fix, a User Update whose artist_pick_track_id
-- pointed at a deleted (or no longer owned) track was rejected outright.
-- Clients resend the full user object on every profile edit, so that stale id
-- rode along with unrelated changes and blocked them: the tx landed on chain,
-- the indexer swallowed the ValidationError, and the user's new profile
-- picture or cover photo silently reverted on refresh. 1,241 accounts were
-- stuck this way in production on 2026-08-11, every one of them from a
-- deleted pick.
--
-- The writer now drops an unresolvable pick instead of rejecting, so those
-- rows self-heal on the owner's next edit. This backfill repairs them now
-- rather than waiting for an edit that many of the affected accounts —
-- dormant for months — may never make.
--
-- Only the dangling reference is cleared; a pick that still resolves is left
-- alone. The value is already non-functional wherever it is cleared, since
-- readers cannot render a deleted track.
UPDATE users u
SET artist_pick_track_id = NULL
WHERE u.is_current
  AND u.artist_pick_track_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM tracks t
    WHERE t.track_id = u.artist_pick_track_id
      AND t.owner_id = u.user_id
      AND t.is_current
      AND t.is_delete = false
  );
