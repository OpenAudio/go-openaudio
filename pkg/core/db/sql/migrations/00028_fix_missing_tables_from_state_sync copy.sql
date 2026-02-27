-- +migrate Up
-- This migration ensures tables exist for nodes that state synced from snapshots
-- created after the original migrations (22, 23, 25, 26, 27) but before these tables
-- were added to the state sync dump list (lines 269-278 in state_sync.go).
--
-- Nodes that state synced during that window have the migration records but not
-- the actual tables, so this migration recreates them safely using IF NOT EXISTS.

-- From migration 00022 (note: without array columns, which were removed in 00023)
CREATE TABLE IF NOT EXISTS core_ern (
    id bigserial primary key,
    address text not null,
    index bigint not null,
    tx_hash text not null,
    sender text not null,
    message_control_type smallint not null,
    raw_message bytea not null,
    raw_acknowledgment bytea not null,
    block_height bigint not null
);

CREATE INDEX IF NOT EXISTS idx_core_ern_address on core_ern (address);
CREATE INDEX IF NOT EXISTS idx_core_ern_block_height on core_ern (block_height);
CREATE INDEX IF NOT EXISTS idx_core_ern_message_control_type on core_ern (message_control_type);
CREATE INDEX IF NOT EXISTS idx_core_ern_sender on core_ern (sender);

-- From migration 00022 - MEAD (Music Encoding and Archival Data) messages
CREATE TABLE IF NOT EXISTS core_mead(
    id bigserial primary key,
    address text not null,
    tx_hash text not null,
    index bigint not null,
    sender text not null,
    resource_addresses text[] default '{}',
    release_addresses text[] default '{}',
    raw_message bytea not null,
    raw_acknowledgment bytea not null,
    block_height bigint not null
);

CREATE INDEX IF NOT EXISTS idx_core_mead_address on core_mead (address);
CREATE INDEX IF NOT EXISTS idx_core_mead_block_height on core_mead (block_height);
CREATE INDEX IF NOT EXISTS idx_core_mead_sender on core_mead (sender);

-- From migration 00022 - PIE (Party Information Entity) messages
CREATE TABLE IF NOT EXISTS core_pie(
    id bigserial primary key,
    address text not null,
    tx_hash text not null,
    index bigint not null,
    sender text not null,
    party_addresses text[] default '{}',
    raw_message bytea not null,
    raw_acknowledgment bytea not null,
    block_height bigint not null
);

CREATE INDEX IF NOT EXISTS idx_core_pie_address on core_pie (address);
CREATE INDEX IF NOT EXISTS idx_core_pie_block_height on core_pie (block_height);
CREATE INDEX IF NOT EXISTS idx_core_pie_sender on core_pie (sender);

-- From migration 00023 - normalized entity tables
CREATE TABLE IF NOT EXISTS core_resources (
    address text primary key,
    ern_address text not null,
    entity_type text not null,
    entity_index integer not null,
    tx_hash text not null,
    block_height bigint not null,
    created_at timestamp default now()
);

CREATE TABLE IF NOT EXISTS core_releases (
    address text primary key,
    ern_address text not null,
    entity_type text not null,
    entity_index integer not null,
    tx_hash text not null,
    block_height bigint not null,
    created_at timestamp default now()
);

CREATE TABLE IF NOT EXISTS core_parties (
    address text primary key,
    ern_address text not null,
    entity_type text not null,
    entity_index integer not null,
    tx_hash text not null,
    block_height bigint not null,
    created_at timestamp default now()
);

CREATE TABLE IF NOT EXISTS core_deals (
    address text primary key,
    ern_address text not null,
    entity_type text not null,
    entity_index integer not null,
    tx_hash text not null,
    block_height bigint not null,
    created_at timestamp default now()
);

CREATE INDEX IF NOT EXISTS idx_core_resources_ern_address on core_resources(ern_address);
CREATE INDEX IF NOT EXISTS idx_core_resources_tx_hash on core_resources(tx_hash);
CREATE INDEX IF NOT EXISTS idx_core_resources_block_height on core_resources(block_height);
CREATE INDEX IF NOT EXISTS idx_core_releases_ern_address on core_releases(ern_address);
CREATE INDEX IF NOT EXISTS idx_core_releases_tx_hash on core_releases(tx_hash);
CREATE INDEX IF NOT EXISTS idx_core_releases_block_height on core_releases(block_height);
CREATE INDEX IF NOT EXISTS idx_core_parties_ern_address on core_parties(ern_address);
CREATE INDEX IF NOT EXISTS idx_core_parties_tx_hash on core_parties(tx_hash);
CREATE INDEX IF NOT EXISTS idx_core_parties_block_height on core_parties(block_height);
CREATE INDEX IF NOT EXISTS idx_core_deals_ern_address on core_deals(ern_address);
CREATE INDEX IF NOT EXISTS idx_core_deals_tx_hash on core_deals(tx_hash);
CREATE INDEX IF NOT EXISTS idx_core_deals_block_height on core_deals(block_height);

-- From migration 00026 - programmatic rewards
CREATE TABLE IF NOT EXISTS core_rewards (
    id bigserial primary key,
    address text not null,
    index bigint not null,
    tx_hash text not null,
    sender text not null,
    reward_id text not null,
    name text not null,
    amount bigint not null,
    claim_authorities text[] default '{}',
    raw_message bytea not null,
    block_height bigint not null,
    created_at timestamp with time zone default now(),
    updated_at timestamp with time zone default now()
);

CREATE INDEX IF NOT EXISTS idx_core_rewards_address on core_rewards (address);
CREATE INDEX IF NOT EXISTS idx_core_rewards_reward_id on core_rewards (reward_id);
CREATE INDEX IF NOT EXISTS idx_core_rewards_block_height on core_rewards (block_height);
CREATE INDEX IF NOT EXISTS idx_core_rewards_sender on core_rewards (sender);
CREATE INDEX IF NOT EXISTS idx_core_rewards_claim_authorities on core_rewards using gin (claim_authorities);

-- From migration 00027 - programmable distribution
CREATE TABLE IF NOT EXISTS core_uploads(
  id bigserial primary key,
  uploader_address text not null,
  cid text not null,
  transcoded_cid text not null,
  upid text not null,
  upload_signature text not null,
  validator_address text not null,
  validator_signature text not null,
  tx_hash text not null,
  block_height bigint not null
);

CREATE INDEX IF NOT EXISTS idx_core_uploads_cid on core_uploads(cid);
CREATE INDEX IF NOT EXISTS idx_core_uploads_transcoded_cid on core_uploads(transcoded_cid);

-- +migrate Down
-- No-op: we never want to drop these tables as part of a rollback.
-- They should have existed from earlier migrations anyway, and dropping them
-- would cause data loss for nodes that have them correctly.

