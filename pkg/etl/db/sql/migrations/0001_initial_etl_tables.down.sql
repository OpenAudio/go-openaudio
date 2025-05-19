drop index if exists etl_blocks_block_height_idx;
drop index if exists etl_blocks_block_time_idx;
drop table if exists etl_blocks;

drop index if exists etl_plays_block_height_idx;
drop index if exists etl_plays_played_at_idx;
drop index if exists etl_plays_track_id_idx;
drop index if exists etl_plays_address_idx;
drop index if exists etl_plays_tx_hash_idx;
drop table if exists etl_plays;

drop index if exists etl_manage_entities_block_height_idx;
drop index if exists etl_manage_entities_tx_hash_idx;
drop table if exists etl_manage_entities;

drop index if exists etl_validator_registrations_block_height_idx;
drop index if exists etl_validator_registrations_tx_hash_idx;
drop table if exists etl_validator_registrations;

drop index if exists etl_validator_deregistrations_block_height_idx;
drop index if exists etl_validator_deregistrations_tx_hash_idx;
drop table if exists etl_validator_deregistrations;
