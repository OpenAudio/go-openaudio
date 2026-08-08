DROP INDEX IF EXISTS ix_subscriptions_entity_type_entity_id;

-- Restore the pre-0038 shape: User subscriptions carried NULL entity_id.
-- This also nulls rows written by the new writers — faithful, since pre-0038
-- readers never consult entity_id for User rows.
UPDATE subscriptions
SET entity_id = NULL
WHERE entity_type = 'User';
