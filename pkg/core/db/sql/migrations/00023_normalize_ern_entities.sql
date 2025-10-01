-- +migrate Up

-- create normalized entity tables that reference back to core_ern

-- resources table - tracks individual sound recordings, videos, etc.
create table core_resources (
    address text primary key,
    ern_address text not null,
    entity_type text not null, -- 'resource'
    entity_index integer not null, -- position in original ern message
    tx_hash text not null,
    block_height bigint not null,
    created_at timestamp default now()
);

-- releases table - tracks albums, singles, etc.
create table core_releases (
    address text primary key,
    ern_address text not null,
    entity_type text not null, -- 'release'
    entity_index integer not null, -- position in original ern message
    tx_hash text not null,
    block_height bigint not null,
    created_at timestamp default now()
);

-- parties table - tracks artists, labels, distributors, etc.
create table core_parties (
    address text primary key,
    ern_address text not null,
    entity_type text not null, -- 'party'
    entity_index integer not null, -- position in original ern message
    tx_hash text not null,
    block_height bigint not null,
    created_at timestamp default now()
);

-- deals table - tracks licensing agreements, contracts, etc.
create table core_deals (
    address text primary key,
    ern_address text not null,
    entity_type text not null, -- 'deal'
    entity_index integer not null, -- position in original ern message
    tx_hash text not null,
    block_height bigint not null,
    created_at timestamp default now()
);

-- create indexes for efficient lookups
create index idx_core_resources_ern_address on core_resources(ern_address);
create index idx_core_resources_tx_hash on core_resources(tx_hash);
create index idx_core_resources_block_height on core_resources(block_height);

create index idx_core_releases_ern_address on core_releases(ern_address);
create index idx_core_releases_tx_hash on core_releases(tx_hash);
create index idx_core_releases_block_height on core_releases(block_height);

create index idx_core_parties_ern_address on core_parties(ern_address);
create index idx_core_parties_tx_hash on core_parties(tx_hash);
create index idx_core_parties_block_height on core_parties(block_height);

create index idx_core_deals_ern_address on core_deals(ern_address);
create index idx_core_deals_tx_hash on core_deals(tx_hash);
create index idx_core_deals_block_height on core_deals(block_height);

-- remove the array columns from core_ern table since we now have normalized tables
alter table core_ern drop column if exists resource_addresses;
alter table core_ern drop column if exists release_addresses;
alter table core_ern drop column if exists party_addresses;
alter table core_ern drop column if exists deal_addresses;

-- +migrate Down

-- restore array columns to core_ern table
alter table core_ern add column resource_addresses text[] default '{}';
alter table core_ern add column release_addresses text[] default '{}';
alter table core_ern add column party_addresses text[] default '{}';
alter table core_ern add column deal_addresses text[] default '{}';

-- drop normalized tables
drop table if exists core_deals;
drop table if exists core_parties;
drop table if exists core_releases;
drop table if exists core_resources;
