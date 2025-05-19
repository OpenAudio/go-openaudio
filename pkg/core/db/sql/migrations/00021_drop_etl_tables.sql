-- +migrate Up
-- Drop indexes for core_etl_tx
drop index if exists core_etl_tx_block_height_idx;
drop index if exists core_etl_tx_created_at_idx;
drop index if exists core_etl_tx_tx_type_idx;

-- Drop indexes for core_etl_tx_manage_entity
drop index if exists core_etl_tx_manage_entity_user_id_idx;
drop index if exists core_etl_tx_manage_entity_entity_type_idx;
drop index if exists core_etl_tx_manage_entity_action_idx;

-- Drop indexes for core_etl_tx_storage_proof_verification
drop index if exists core_etl_tx_storage_proof_verification_height_idx;

-- Drop indexes for core_etl_tx_storage_proof
drop index if exists core_etl_tx_storage_proof_address_idx;
drop index if exists core_etl_tx_storage_proof_height_idx;

-- Drop indexes for core_etl_tx_sla_rollup
drop index if exists core_etl_tx_sla_rollup_block_range_idx;
drop index if exists core_etl_tx_sla_rollup_timestamp_idx;

-- Drop indexes for core_etl_tx_validator_deregistration
drop index if exists core_etl_tx_validator_deregistration_comet_address_idx;

-- Drop indexes for core_etl_tx_validator_registration
drop index if exists core_etl_tx_validator_registration_node_type_idx;
drop index if exists core_etl_tx_validator_registration_comet_address_idx;
drop index if exists core_etl_tx_validator_registration_endpoint_idx;

-- Drop indexes for core_etl_tx_plays
drop index if exists core_etl_tx_plays_track_id_idx;
drop index if exists core_etl_tx_plays_user_id_idx;
drop index if exists core_etl_tx_plays_played_at_idx;
drop index if exists core_etl_tx_plays_country_idx;
drop index if exists core_etl_tx_plays_region_idx;
drop index if exists core_etl_tx_plays_city_idx;

-- Drop the optimized indexes
DROP INDEX IF EXISTS core_etl_tx_plays_city_region_country_plays_idx;
DROP INDEX IF EXISTS core_etl_tx_plays_region_country_plays_idx;
DROP INDEX IF EXISTS core_etl_tx_plays_country_plays_idx;

-- Drop tables
drop table if exists core_etl_tx;
drop table if exists core_etl_tx_duplicates;
drop table if exists core_etl_tx_manage_entity;
drop table if exists core_etl_tx_storage_proof_verification;
drop table if exists core_etl_tx_storage_proof;
drop table if exists core_etl_tx_sla_rollup;
drop table if exists core_etl_tx_validator_deregistration;
drop table if exists core_etl_tx_validator_registration;
drop table if exists core_etl_tx_plays; 


-- +migrate Down
