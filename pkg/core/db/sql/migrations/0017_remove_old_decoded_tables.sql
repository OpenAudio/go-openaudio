-- +migrate Up

-- Drop indexes for core_tx_decoded_plays
drop index if exists core_tx_decoded_plays_track_id_idx;
drop index if exists core_tx_decoded_plays_user_id_idx;
drop index if exists core_tx_decoded_plays_played_at_idx;
drop index if exists core_tx_decoded_plays_country_idx;
drop index if exists core_tx_decoded_plays_region_idx;
drop index if exists core_tx_decoded_plays_city_idx;

-- Drop indexes for core_tx_decoded
drop index if exists core_tx_decoded_tx_type_idx;
drop index if exists core_tx_decoded_tx_hash_idx;
drop index if exists core_tx_decoded_block_height_idx;

-- Drop tables
drop table if exists core_tx_decoded_plays;
drop table if exists core_tx_decoded;

-- +migrate Down
-- No down migration needed as we're removing old tables 