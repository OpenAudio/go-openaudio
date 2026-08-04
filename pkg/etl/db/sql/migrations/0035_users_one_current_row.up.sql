-- One current row per user, enforced.
--
-- 0030 added this arbiter for reposts/saves/follows/subscriptions on the
-- grounds that "only one current row per identity is an existing invariant".
-- users holds the same invariant — Create inserts a single is_current row and
-- Update mutates it in place — but was left out, and it is the one table where
-- the invariant has actually been violated.
--
-- users_pkey is (user_id, txhash), so a second Create for a user that already
-- exists inserts alongside rather than conflicting: the create path has no
-- existence check. Five users picked up a duplicate that way on a production
-- clone (3.15M users), spread from 2025-06 to 2026-06 — a slow drip, not a
-- one-off. Each pair shares created_at and sits a few blocks apart.
--
-- Five rows, but the blast radius is not five rows: anything joining an entity
-- to its owner's wallet fans out and silently duplicates every entity those
-- users own. genesis-writer carried a DISTINCT ON subquery in all fifteen of
-- its joins for exactly this reason.
--
-- Unlike 0030 this needs a backfill first, since violations already exist.
-- The losers are deleted rather than demoted: users no longer keeps versioned
-- history — the in-place writes mean a production clone has zero is_current =
-- false rows — so demoting would leave behind a category of row that nothing
-- reads and that no other code path produces. Deleting is safe: no foreign key
-- references users, and its triggers are INSERT (on_user) or INSERT OR UPDATE
-- (trg_users), so neither fires.
--
-- Winner is the highest blocknumber, matching how consumers already pick the
-- live row; verified against a production clone, where it also keeps
-- is_deactivated = true for user 666149592, the later of that pair's states.

WITH ranked AS (
    SELECT
        user_id,
        txhash,
        row_number() OVER (
            PARTITION BY user_id
            ORDER BY blocknumber DESC NULLS LAST, updated_at DESC NULLS LAST, txhash DESC
        ) AS rn
    FROM users
    WHERE is_current = true
)
DELETE FROM users u
USING ranked r
WHERE u.user_id = r.user_id
  AND u.txhash = r.txhash
  AND r.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS users_current_uniq_idx
  ON users (user_id) WHERE is_current = true;
