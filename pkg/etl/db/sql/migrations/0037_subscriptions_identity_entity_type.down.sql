-- Not recreating the 0030 (subscriber_id, user_id) definition: data written
-- under this migration may legitimately contain cross-type pairs that violate
-- the narrower key. Matches 0030.down, which likewise only drops.
DROP INDEX IF EXISTS subscriptions_current_uniq_idx;
