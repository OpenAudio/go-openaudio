-- A subscription's identity must include entity_type. subscriptions.user_id is
-- overloaded: for User subscriptions it holds the followed user's id, for Event
-- subscriptions it holds the event id (mirrored into entity_id). User ids and
-- event ids are allocated independently, so user N and event N can both exist,
-- and a subscriber may legitimately hold both subscriptions at once. The 0030
-- index keyed only (subscriber_id, user_id), conflating the two: Subscribe
-- rejected the second subscription outright, and the Follow auto-subscribe
-- upsert could land on an Event row and tombstone it on unfollow.
--
-- The 0030 index is strictly tighter than this one, so existing data cannot
-- violate the widened key and no dedupe is needed.

DROP INDEX IF EXISTS subscriptions_current_uniq_idx;

CREATE UNIQUE INDEX IF NOT EXISTS subscriptions_current_uniq_idx
  ON subscriptions (subscriber_id, user_id, entity_type) WHERE is_current = true;
