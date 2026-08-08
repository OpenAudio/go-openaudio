-- Make entity_id the canonical subscription target for both entity types.
-- 0029 added entity_id but only Event subscriptions populate it; User rows
-- leave it NULL and readers must fall back to the overloaded user_id column.
-- The writers now set entity_id = user_id for User subscriptions; this
-- backfills the rows written before that, so readers can migrate to
-- (entity_type, entity_id) and user_id can degrade to a legacy mirror.
UPDATE subscriptions
SET entity_id = user_id
WHERE entity_type = 'User' AND entity_id IS NULL;

-- Reader lookup index for the canonical target, the entity_id counterpart of
-- ix_subscriptions_user_id (0007). Non-unique on purpose: the current-row
-- invariant stays enforced by subscriptions_current_uniq_idx on user_id, which
-- is NOT NULL — entity_id isn't yet, and NULLs would bypass a unique key here.
CREATE INDEX IF NOT EXISTS ix_subscriptions_entity_type_entity_id
  ON subscriptions (entity_type, entity_id);
