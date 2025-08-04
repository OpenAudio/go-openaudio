-- Drop materialized views first
drop materialized view if exists mv_dashboard_transaction_types;
drop materialized view if exists mv_dashboard_transaction_stats;

-- Drop triggers
drop trigger if exists trigger_notify_new_plays on etl_plays;
drop trigger if exists trigger_notify_new_block on etl_blocks;

-- Drop functions
drop function if exists notify_new_plays();
drop function if exists notify_new_block();

-- Drop tables (in reverse order of dependencies)
drop table if exists etl_sla_node_reports;
drop table if exists etl_storage_proof_verifications;
drop table if exists etl_storage_proofs;
drop table if exists etl_validator_misbehavior_deregistrations;
drop table if exists etl_sla_rollups;
drop table if exists etl_validator_deregistrations;
drop table if exists etl_validator_registrations;
drop table if exists etl_validators;
drop table if exists etl_manage_entities;
drop table if exists etl_plays;
drop table if exists etl_blocks;
drop table if exists etl_transactions;
drop table if exists etl_addresses;
