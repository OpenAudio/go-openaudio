-- Upsert support for social writes (reposts/saves/follows/subscriptions).
-- The entity_manager now upserts the single is_current row in place instead of
-- demote-then-insert, so it needs a unique arbiter on the entity identity.
-- Partial on is_current=true: only one current row per identity is an existing
-- invariant, so historical demoted rows are ignored and no dedup/backfill runs.

CREATE UNIQUE INDEX IF NOT EXISTS reposts_current_uniq_idx
  ON reposts (user_id, repost_item_id, repost_type) WHERE is_current = true;

CREATE UNIQUE INDEX IF NOT EXISTS saves_current_uniq_idx
  ON saves (user_id, save_item_id, save_type) WHERE is_current = true;

CREATE UNIQUE INDEX IF NOT EXISTS follows_current_uniq_idx
  ON follows (follower_user_id, followee_user_id) WHERE is_current = true;

CREATE UNIQUE INDEX IF NOT EXISTS subscriptions_current_uniq_idx
  ON subscriptions (subscriber_id, user_id) WHERE is_current = true;
