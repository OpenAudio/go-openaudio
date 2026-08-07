-- Make entity_id the canonical subscription target for both entity types.
-- 0029 added entity_id but only Event subscriptions populate it; User rows
-- leave it NULL and readers must fall back to the overloaded user_id column.
-- The writers now set entity_id = user_id for User subscriptions; this
-- backfills the rows written before that, so readers can migrate to
-- (entity_type, entity_id) and user_id can degrade to a legacy mirror.
UPDATE subscriptions
SET entity_id = user_id
WHERE entity_type = 'User' AND entity_id IS NULL;
