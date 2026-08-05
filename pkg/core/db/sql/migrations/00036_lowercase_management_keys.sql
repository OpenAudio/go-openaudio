-- +migrate Up
-- Stream signatures recover to EIP-55 checksummed addresses (go-ethereum's
-- Address.Hex()), but management_keys.address was stored verbatim from the track
-- metadata's access_authorities, in whatever casing the publisher used. Streaming
-- auth now lowercases both sides, so normalize rows written before that change --
-- otherwise a track whose authorities were published checksummed stops matching.
--
-- Nodes that state-sync from a snapshot predating this migration inherit both the
-- old casing and core_db_migrations, so it will not re-run for them. That resolves
-- on its own once snapshot sources have upgraded.
update management_keys set address = lower(address) where address <> lower(address);

-- +migrate Down
-- No-op: the original casing is not recoverable, and a lowercase address is still
-- valid input to the pre-normalization exact-match comparison.
select 1;
