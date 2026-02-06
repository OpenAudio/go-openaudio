-- Tables for ETL service

-- Storage proof status enum
create type etl_proof_status as enum ('unresolved', 'pass', 'fail');

create table if not exists etl_addresses(
  id serial primary key,
  address text not null,
  pub_key bytea,
  first_seen_block_height bigint,
  created_at timestamp not null
);


create table if not exists etl_transactions(
  id serial primary key,
  tx_hash text not null,
  block_height bigint not null,
  tx_index integer not null,
  tx_type text not null,
  address text,
  created_at timestamp not null
);

create table if not exists etl_blocks(
  id serial primary key,
  proposer_address text not null,
  block_height bigint not null,
  block_time timestamp not null
);

create table if not exists etl_plays(
  id serial primary key,
  user_id text not null,
  track_id text not null,
  city text not null,
  region text not null,
  country text not null,
  played_at timestamp not null,
  block_height bigint not null,
  tx_hash text not null,
  listened_at timestamp not null,
  recorded_at timestamp not null
);

create table if not exists etl_manage_entities(
  id serial primary key,
  address text not null,
  entity_type text not null,
  entity_id bigint not null,
  action text not null,
  metadata text,
  signature text not null,
  signer text not null,
  nonce text not null,
  block_height bigint not null,
  tx_hash text not null,
  created_at timestamp not null
);

create table if not exists etl_validator_registrations(
  id serial primary key,
  address text not null,
  endpoint text not null,
  comet_address text not null,
  eth_block text not null,
  node_type text not null,
  spid text not null,
  comet_pubkey bytea not null,
  voting_power bigint not null,
  block_height bigint not null,
  tx_hash text not null
);

create table if not exists etl_validator_deregistrations(
  id serial primary key,
  comet_address text not null,
  comet_pubkey bytea not null,
  block_height bigint not null,
  tx_hash text not null
);

create table if not exists etl_sla_rollups(
  id serial primary key,
  block_start bigint not null,
  block_end bigint not null,
  block_height bigint not null,
  validator_count integer not null,
  block_quota integer not null,
  bps float not null,
  tps float not null,
  tx_hash text not null,
  created_at timestamp not null
);

create table if not exists etl_sla_node_reports(
  id serial primary key,
  sla_rollup_id integer not null references etl_sla_rollups(id),
  address text not null,
  num_blocks_proposed integer not null,
  challenges_received integer not null,
  challenges_failed integer not null,
  block_height bigint not null,
  tx_hash text not null,
  created_at timestamp not null
);

create table if not exists etl_validator_misbehavior_deregistrations(
  id serial primary key,
  comet_address text not null,
  pub_key bytea not null,
  block_height bigint not null,
  tx_hash text not null,
  created_at timestamp not null
);

create table if not exists etl_storage_proofs(
  id serial primary key,
  height bigint not null,
  address text not null,
  prover_addresses text[] not null,
  cid text not null,
  proof_signature bytea,
  proof bytea,
  status etl_proof_status not null default 'unresolved',
  block_height bigint not null,
  tx_hash text not null,
  created_at timestamp not null
);

create table if not exists etl_storage_proof_verifications(
  id serial primary key,
  height bigint not null,
  proof bytea not null,
  block_height bigint not null,
  tx_hash text not null,
  created_at timestamp not null
);

create table if not exists etl_validators(
  id serial primary key,
  address text not null,
  endpoint text not null unique,
  comet_address text not null,
  node_type text not null,
  spid text not null,
  voting_power bigint not null,
  status text not null,
  registered_at bigint not null,
  deregistered_at bigint,
  created_at timestamp not null,
  updated_at timestamp not null
);

-- Indexes
create index if not exists etl_transactions_address_idx on etl_transactions(address);
create index if not exists etl_transactions_tx_type_idx on etl_transactions(tx_type);
create index if not exists etl_transactions_created_at_idx on etl_transactions(created_at);

-- Additional comprehensive indexes for query optimization

-- Primary key and ordering indexes
create index if not exists etl_blocks_id_desc_idx on etl_blocks(id desc);
create index if not exists etl_transactions_id_desc_idx on etl_transactions(id desc);
create index if not exists etl_blocks_block_height_desc_idx on etl_blocks(block_height desc);
create index if not exists etl_transactions_block_height_desc_idx on etl_transactions(block_height desc, tx_index desc);

-- Cursor pagination composite indexes (block_height, id) for efficient pagination
create index if not exists etl_transactions_cursor_idx on etl_transactions(block_height, id);
create index if not exists etl_plays_cursor_idx on etl_plays(block_height, id);
create index if not exists etl_manage_entities_cursor_idx on etl_manage_entities(block_height, id);
create index if not exists etl_validator_registrations_cursor_idx on etl_validator_registrations(block_height, id);
create index if not exists etl_validator_deregistrations_cursor_idx on etl_validator_deregistrations(block_height, id);
create index if not exists etl_sla_rollups_cursor_idx on etl_sla_rollups(block_height, id);
create index if not exists etl_sla_node_reports_cursor_idx on etl_sla_node_reports(block_height, id);
create index if not exists etl_validator_misbehavior_deregistrations_cursor_idx on etl_validator_misbehavior_deregistrations(block_height, id);
create index if not exists etl_storage_proofs_cursor_idx on etl_storage_proofs(block_height, id);
create index if not exists etl_storage_proof_verifications_cursor_idx on etl_storage_proof_verifications(block_height, id);

-- Hash lookup indexes for O(1) access
create unique index if not exists etl_transactions_tx_hash_idx on etl_transactions(tx_hash);
create index if not exists etl_plays_tx_hash_idx on etl_plays(tx_hash);
create index if not exists etl_manage_entities_tx_hash_idx on etl_manage_entities(tx_hash);
create index if not exists etl_validator_registrations_tx_hash_idx on etl_validator_registrations(tx_hash);
create index if not exists etl_validator_deregistrations_tx_hash_idx on etl_validator_deregistrations(tx_hash);
create index if not exists etl_sla_rollups_tx_hash_idx on etl_sla_rollups(tx_hash);
create index if not exists etl_storage_proofs_tx_hash_idx on etl_storage_proofs(tx_hash);
create index if not exists etl_storage_proof_verifications_tx_hash_idx on etl_storage_proof_verifications(tx_hash);

-- Block queries
create index if not exists etl_blocks_block_height_idx on etl_blocks(block_height);
create index if not exists etl_blocks_block_time_idx on etl_blocks(block_time);
create index if not exists etl_blocks_block_time_range_idx on etl_blocks(block_time, block_height);

-- Validator-specific indexes
create index if not exists etl_validators_status_idx on etl_validators(status);
create index if not exists etl_validators_comet_address_idx on etl_validators(comet_address);
create index if not exists etl_validators_address_lower_idx on etl_validators(lower(address));
create index if not exists etl_validators_comet_address_lower_idx on etl_validators(lower(comet_address));
create index if not exists etl_validator_registrations_comet_address_idx on etl_validator_registrations(comet_address);
create index if not exists etl_validator_deregistrations_comet_address_idx on etl_validator_deregistrations(comet_address);

-- Address-based queries with case-insensitive matching
create index if not exists etl_transactions_address_lower_idx on etl_transactions(lower(address));
create index if not exists etl_sla_node_reports_address_lower_idx on etl_sla_node_reports(lower(address));

-- Storage proof queries
create index if not exists etl_storage_proofs_height_idx on etl_storage_proofs(height);
create index if not exists etl_storage_proofs_height_address_idx on etl_storage_proofs(height, address);
create index if not exists etl_storage_proofs_height_range_idx on etl_storage_proofs(height, address) where height >= 0;

-- SLA rollup and node report relationships
create index if not exists etl_sla_node_reports_sla_rollup_id_idx on etl_sla_node_reports(sla_rollup_id);
create index if not exists etl_sla_node_reports_address_sla_rollup_idx on etl_sla_node_reports(address, sla_rollup_id);
create index if not exists etl_sla_rollups_block_height_desc_idx on etl_sla_rollups(block_height desc, id desc);

-- Complex query optimizations for GetTransactionsByAddress and GetAllActiveValidatorsWithRecentRollups
create index if not exists etl_transactions_address_filter_idx on etl_transactions(lower(address), tx_type, created_at, block_height desc, tx_index desc);
create index if not exists etl_validators_active_reports_idx on etl_validators(status, comet_address) where status = 'active';

-- Manage entities for transaction relation queries
create index if not exists etl_manage_entities_tx_hash_action_entity_idx on etl_manage_entities(tx_hash, action, entity_type);

-- Time-based analysis indexes for dashboard views
create index if not exists etl_transactions_created_at_type_idx on etl_transactions(created_at, tx_type);
create index if not exists etl_blocks_block_time_height_idx on etl_blocks(block_time desc, block_height desc);

-- Covering indexes for frequently accessed columns to avoid table lookups
create index if not exists etl_validators_status_covering_idx on etl_validators(status, comet_address, endpoint, node_type, spid, voting_power) where status = 'active';
create index if not exists etl_sla_rollups_latest_covering_idx on etl_sla_rollups(block_height desc, id desc, block_start, block_end, validator_count, block_quota, bps, tps);

-- Partial indexes for specific query patterns
create index if not exists etl_storage_proofs_status_unresolved_idx on etl_storage_proofs(height, address) where status = 'unresolved';
create index if not exists etl_storage_proofs_status_fail_idx on etl_storage_proofs(height, address) where status = 'fail';

-- Pgnotify triggers

-- Function to notify when a new block is inserted
create or replace function notify_new_block()
returns trigger as $$
begin
  perform pg_notify('new_block', json_build_object(
    'block_height', new.block_height,
    'proposer_address', new.proposer_address
  )::text);
  return new;
end;
$$ language plpgsql;

-- Function to notify when new plays are inserted
create or replace function notify_new_plays()
returns trigger as $$
begin
  perform pg_notify('new_plays', json_build_object(
    'user_id', new.user_id,
    'track_id', new.track_id,
    'city', new.city,
    'region', new.region,
    'country', new.country,
    'block_height', new.block_height
  )::text);
  return new;
end;
$$ language plpgsql;

-- Trigger for new blocks
create trigger trigger_notify_new_block
  after insert on etl_blocks
  for each row
  execute function notify_new_block();

-- Trigger for new plays
create trigger trigger_notify_new_plays
  after insert on etl_plays
  for each row
  execute function notify_new_plays();

-- Materialized views for dashboard stats
-- These use the latest indexed block timestamp as "now" so syncing nodes have updating data

-- Transaction time-based statistics
create materialized view mv_dashboard_transaction_stats as
with latest_block_time as (
  select block_time from etl_blocks order by block_height desc limit 1
),
time_periods as (
  select 
    lbt.block_time as now_time,
    lbt.block_time - interval '24 hours' as h24_ago,
    lbt.block_time - interval '48 hours' as h48_ago,
    lbt.block_time - interval '7 days' as d7_ago,
    lbt.block_time - interval '30 days' as d30_ago
  from latest_block_time lbt
)
select
  -- Current 24h count
  count(*) filter (where t.created_at >= tp.h24_ago) as transactions_24h,
  -- Previous 24h count (for percentage change calculation)
  count(*) filter (where t.created_at >= tp.h48_ago and t.created_at < tp.h24_ago) as transactions_previous_24h,
  -- 7 day count
  count(*) filter (where t.created_at >= tp.d7_ago) as transactions_7d,
  -- 30 day count  
  count(*) filter (where t.created_at >= tp.d30_ago) as transactions_30d,
  -- Total transactions
  count(*) as total_transactions
from time_periods tp
cross join etl_transactions t
where t.created_at <= tp.now_time;

-- Transaction type breakdown
create materialized view mv_dashboard_transaction_types as
with latest_block_time as (
  select block_time from etl_blocks order by block_height desc limit 1
)
select 
  t.tx_type,
  count(*) as transaction_count
from etl_transactions t
cross join latest_block_time lbt
where t.created_at <= lbt.block_time
group by t.tx_type
order by transaction_count desc;

-- Indexes for better performance
create index on mv_dashboard_transaction_stats using btree (transactions_24h);
create index on mv_dashboard_transaction_types using btree (tx_type, transaction_count);
