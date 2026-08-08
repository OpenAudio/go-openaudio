-- Run AFTER a replay completes, before serving traffic. Recreates exactly
-- the set dropped by drop-serving-indexes.sql; definitions are the
-- pg_get_indexdef output captured on the 2026-08 run. If a migration
-- changes one of these indexes, update both files together.
SET maintenance_work_mem = '4GB';
SET max_parallel_maintenance_workers = 4;

CREATE INDEX IF NOT EXISTS etl_transactions_address_filter_idx ON public.etl_transactions USING btree (lower(address), tx_type, created_at, block_height DESC, tx_index DESC);
CREATE INDEX IF NOT EXISTS etl_transactions_address_idx ON public.etl_transactions USING btree (address);
CREATE INDEX IF NOT EXISTS etl_transactions_address_lower_idx ON public.etl_transactions USING btree (lower(address));
CREATE INDEX IF NOT EXISTS etl_transactions_block_height_desc_idx ON public.etl_transactions USING btree (block_height DESC, tx_index DESC);
CREATE INDEX IF NOT EXISTS etl_transactions_created_at_idx ON public.etl_transactions USING btree (created_at);
CREATE INDEX IF NOT EXISTS etl_transactions_created_at_type_idx ON public.etl_transactions USING btree (created_at, tx_type);
CREATE INDEX IF NOT EXISTS etl_transactions_cursor_idx ON public.etl_transactions USING btree (block_height, id);
CREATE INDEX IF NOT EXISTS etl_transactions_id_desc_idx ON public.etl_transactions USING btree (id DESC);
CREATE INDEX IF NOT EXISTS etl_transactions_tx_type_idx ON public.etl_transactions USING btree (tx_type);
CREATE INDEX IF NOT EXISTS etl_manage_entities_cursor_idx ON public.etl_manage_entities USING btree (block_height, id);
CREATE INDEX IF NOT EXISTS etl_manage_entities_tx_hash_action_entity_idx ON public.etl_manage_entities USING btree (tx_hash, action, entity_type);
CREATE INDEX IF NOT EXISTS etl_manage_entities_tx_hash_idx ON public.etl_manage_entities USING btree (tx_hash);
CREATE INDEX IF NOT EXISTS etl_plays_cursor_idx ON public.etl_plays USING btree (block_height, id);
CREATE INDEX IF NOT EXISTS etl_plays_tx_hash_idx ON public.etl_plays USING btree (tx_hash);
CREATE INDEX IF NOT EXISTS follows_inbound_idx ON public.follows USING btree (followee_user_id, follower_user_id, is_delete);
CREATE INDEX IF NOT EXISTS reposts_item_idx ON public.reposts USING btree (repost_item_id, repost_type, user_id, is_delete);
CREATE INDEX IF NOT EXISTS reposts_user_idx ON public.reposts USING btree (user_id, repost_type, repost_item_id, created_at, is_delete);
CREATE INDEX IF NOT EXISTS saves_item_idx ON public.saves USING btree (save_item_id, save_type, user_id, is_delete);
CREATE INDEX IF NOT EXISTS ix_subscriptions_user_id ON public.subscriptions USING btree (user_id);
CREATE INDEX IF NOT EXISTS tracks_track_cid_idx ON public.tracks USING btree (track_cid, is_delete);
CREATE INDEX IF NOT EXISTS users_new_handle_lc_idx ON public.users USING btree (handle_lc);
CREATE INDEX IF NOT EXISTS users_new_wallet_idx ON public.users USING btree (wallet);
