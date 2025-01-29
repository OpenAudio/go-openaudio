-- +migrate Up
create table if not exists core_blocks(
  rowid bigserial primary key,
  height bigint not null,
  chain_id text not null,
  hash text not null,
  proposer text not null,
  created_at timestamp not null,

  unique (height, chain_id)
);

create index idx_core_blocks_height on core_blocks(height);
create index idx_core_blocks_chain_id on core_blocks(chain_id);
create index idx_core_blocks_hash on core_blocks(hash);
create index idx_core_blocks_proposer on core_blocks(proposer);
create index idx_core_blocks_created_at on core_blocks(created_at);

create table if not exists core_transactions(
  rowid bigserial primary key,
  block_id bigint not null,
  index integer not null,
  tx_hash text not null,
  transaction bytea not null,
  created_at timestamp not null,

  unique (block_id, index)
);

CREATE index idx_core_transactions_tx_hash_lower ON core_transactions (LOWER(tx_hash));
create index idx_core_transactions_block_id on core_transactions(block_id);
create index idx_core_transactions_tx_hash on core_transactions(tx_hash);
create index idx_core_transactions_created_at on core_transactions(created_at);

-- +migrate Down
drop index if exists idx_core_transactions_created_at;
drop index if exists idx_core_transactions_tx_hash;
drop index if exists idx_core_transactions_block_id;
drop index if exists idx_core_transactions_tx_hash_lower;

drop index if exists idx_core_blocks_created_at;
drop index if exists idx_core_blocks_proposer;
drop index if exists idx_core_blocks_hash;
drop index if exists idx_core_blocks_chain_id;
drop index if exists idx_core_blocks_height;

drop table if exists core_transactions;
drop table if exists core_blocks;
