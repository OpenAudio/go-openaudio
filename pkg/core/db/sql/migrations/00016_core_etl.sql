-- +migrate Up

create table if not exists core_etl_tx (
    id bigserial primary key,
    block_height bigint not null,
    tx_index integer not null,
    tx_hash text not null,
    tx_type text not null,
    tx_data jsonb not null,
    created_at timestamp with time zone not null,
    unique(block_height, tx_index),
    unique(tx_hash)
);

create index if not exists core_etl_tx_tx_type_idx on core_etl_tx(tx_type);
create index if not exists core_etl_tx_created_at_idx on core_etl_tx(created_at);
create index if not exists core_etl_tx_block_height_idx on core_etl_tx(block_height);

create table if not exists core_etl_tx_plays (
    id bigserial primary key,
    tx_hash text not null,
    user_id text not null,
    track_id text not null,
    played_at timestamp with time zone not null,
    signature text not null,
    city text,
    region text,
    country text,
    created_at timestamp with time zone not null,
    unique(tx_hash, user_id, track_id)
);

create table if not exists core_etl_tx_validator_registration (
    id bigserial primary key,
    tx_hash text not null,
    endpoint text not null,
    comet_address text not null,
    eth_block text not null,
    node_type text not null,
    sp_id text not null,
    pub_key bytea not null,
    power bigint not null,
    created_at timestamp with time zone not null,
    unique(tx_hash)
);

create table if not exists core_etl_tx_validator_deregistration (
    id bigserial primary key,
    tx_hash text not null,
    comet_address text not null,
    pub_key bytea not null,
    created_at timestamp with time zone not null,
    unique(tx_hash)
);

create table if not exists core_etl_tx_sla_rollup (
    id bigserial primary key,
    tx_hash text not null,
    block_start bigint not null,
    block_end bigint not null,
    timestamp timestamp with time zone not null,
    created_at timestamp with time zone not null,
    unique(tx_hash)
);

create table if not exists core_etl_tx_storage_proof (
    id bigserial primary key,
    tx_hash text not null,
    height bigint not null,
    address text not null,
    cid text,
    proof_signature bytea,
    prover_addresses text[] not null,
    created_at timestamp with time zone not null,
    unique(tx_hash)
);

create table if not exists core_etl_tx_storage_proof_verification (
    id bigserial primary key,
    tx_hash text not null,
    height bigint not null,
    proof bytea not null,
    created_at timestamp with time zone not null,
    unique(tx_hash)
);

create table if not exists core_etl_tx_manage_entity (
    id bigserial primary key,
    tx_hash text not null,
    user_id bigint not null,
    entity_type text not null,
    entity_id bigint not null,
    action text not null,
    metadata text not null,
    signature text not null,
    signer text not null,
    nonce text not null,
    created_at timestamp with time zone not null,
    unique(tx_hash)
);

create table if not exists core_etl_tx_duplicates (
    id bigserial primary key,
    tx_hash text not null,
    table_name text not null,
    duplicate_type text not null,
    created_at timestamp with time zone not null default now(),
    unique(tx_hash, table_name)
);

-- Indexes for core_etl_tx_plays
create index if not exists core_etl_tx_plays_city_idx on core_etl_tx_plays(city);
create index if not exists core_etl_tx_plays_region_idx on core_etl_tx_plays(region);
create index if not exists core_etl_tx_plays_country_idx on core_etl_tx_plays(country);
create index if not exists core_etl_tx_plays_played_at_idx on core_etl_tx_plays(played_at);
create index if not exists core_etl_tx_plays_user_id_idx on core_etl_tx_plays(user_id);
create index if not exists core_etl_tx_plays_track_id_idx on core_etl_tx_plays(track_id);

-- Indexes for core_etl_tx_validator_registration
create index if not exists core_etl_tx_validator_registration_endpoint_idx on core_etl_tx_validator_registration(endpoint);
create index if not exists core_etl_tx_validator_registration_comet_address_idx on core_etl_tx_validator_registration(comet_address);
create index if not exists core_etl_tx_validator_registration_node_type_idx on core_etl_tx_validator_registration(node_type);

-- Indexes for core_etl_tx_validator_deregistration
create index if not exists core_etl_tx_validator_deregistration_comet_address_idx on core_etl_tx_validator_deregistration(comet_address);

-- Indexes for core_etl_tx_sla_rollup
create index if not exists core_etl_tx_sla_rollup_timestamp_idx on core_etl_tx_sla_rollup(timestamp);
create index if not exists core_etl_tx_sla_rollup_block_range_idx on core_etl_tx_sla_rollup(block_start, block_end);

-- Indexes for core_etl_tx_storage_proof
create index if not exists core_etl_tx_storage_proof_height_idx on core_etl_tx_storage_proof(height);
create index if not exists core_etl_tx_storage_proof_address_idx on core_etl_tx_storage_proof(address);

-- Indexes for core_etl_tx_storage_proof_verification
create index if not exists core_etl_tx_storage_proof_verification_height_idx on core_etl_tx_storage_proof_verification(height);

-- Indexes for core_etl_tx_manage_entity
create index if not exists core_etl_tx_manage_entity_action_idx on core_etl_tx_manage_entity(action);
create index if not exists core_etl_tx_manage_entity_entity_type_idx on core_etl_tx_manage_entity(entity_type);
create index if not exists core_etl_tx_manage_entity_user_id_idx on core_etl_tx_manage_entity(user_id);

-- +migrate Down
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