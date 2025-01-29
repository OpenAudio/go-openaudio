-- +migrate Up

-- remove old tables from the comet based indexer
-- these will be replaced with new schemas and inserted at finalize/commit step
drop index if exists idx_core_blocks_height_chain;
drop view if exists block_events;
drop view if exists tx_events;
drop view if exists event_attributes;
drop table if exists core_attributes;
drop table if exists core_events;
drop table if exists core_tx_results;
drop table if exists core_blocks;

-- +migrate Down
