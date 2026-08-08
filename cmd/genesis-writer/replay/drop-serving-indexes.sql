-- Run AFTER the ETL migrations, BEFORE replaying a genesis chain.
-- Reverse with recreate-serving-indexes.sql when indexing completes.
--
-- These indexes serve API queries. During a replay nothing reads them, but
-- every one of ~58M inserts maintains them -- and their pages evict the ones
-- that ARE hot, turning inserts into random reads. On the 2026-08 run
-- dropping this set was worth 1.85x on its own.
--
-- Every index below recorded ZERO scans across a full 58M-tx replay
-- (measured from pg_stat_user_indexes; see "Re-measuring the drop list" in
-- README.md). Two things about the list that look wrong but are not:
--
--   * users_new_wallet_idx and users_new_handle_lc_idx ARE dropped even
--     though the live indexing path looks up users by wallet and handle.
--     Genesis-migration transactions skip signer validation, so those
--     lookups never fire on this workload. Do not reuse this list for a
--     replay of live (non-migration) traffic.
--   * Primary keys and unique indexes are never dropped: the former may
--     back ON CONFLICT, the latter are upsert arbiters.

DROP INDEX IF EXISTS etl_transactions_address_filter_idx;
DROP INDEX IF EXISTS etl_transactions_address_idx;
DROP INDEX IF EXISTS etl_transactions_address_lower_idx;
DROP INDEX IF EXISTS etl_transactions_block_height_desc_idx;
DROP INDEX IF EXISTS etl_transactions_created_at_idx;
DROP INDEX IF EXISTS etl_transactions_created_at_type_idx;
DROP INDEX IF EXISTS etl_transactions_cursor_idx;
DROP INDEX IF EXISTS etl_transactions_id_desc_idx;
DROP INDEX IF EXISTS etl_transactions_tx_type_idx;
DROP INDEX IF EXISTS etl_manage_entities_cursor_idx;
DROP INDEX IF EXISTS etl_manage_entities_tx_hash_action_entity_idx;
DROP INDEX IF EXISTS etl_manage_entities_tx_hash_idx;
DROP INDEX IF EXISTS etl_plays_cursor_idx;
DROP INDEX IF EXISTS etl_plays_tx_hash_idx;
DROP INDEX IF EXISTS follows_inbound_idx;
DROP INDEX IF EXISTS reposts_item_idx;
DROP INDEX IF EXISTS reposts_user_idx;
DROP INDEX IF EXISTS saves_item_idx;
DROP INDEX IF EXISTS ix_subscriptions_user_id;
DROP INDEX IF EXISTS tracks_track_cid_idx;
DROP INDEX IF EXISTS users_new_handle_lc_idx;
DROP INDEX IF EXISTS users_new_wallet_idx;
