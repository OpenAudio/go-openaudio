-- +migrate Up
-- Undo two audius-mainnet-beta auth projections that the user-metadata rule
-- (server/auth_user_rules.go) refuses. The rule applies to every height, so a
-- node replaying the chain never applies these txs; validators that applied
-- them under the old rules are brought to the same state here. These are the
-- only mirrored User Create/Update txs the rule refuses that changed core
-- state. Matching on every identifying column makes this a no-op elsewhere.

-- A user create with an illegal handle. The indexer never created the user,
-- but core did, and the leftover row held its wallet against that wallet's
-- later valid create. It has no entities, grants, apps or cid claims.
DELETE FROM core_auth_users
WHERE user_id = 243004782
  AND wallet = '0x01674a90568b9df4ea42cb8030801fb45db47986'
  AND handle_lc !~ '^[a-z0-9_.]+$';

-- A user update to an illegal handle. Core recorded the new handle; the
-- indexer kept the previous one.
UPDATE core_auth_users
SET handle_lc = 'i64probemtorsuilx'
WHERE user_id = 990000010
  AND handle_lc = 'i64probe-update';

-- +migrate Down
-- The repaired rows are not restored.
select 1;
