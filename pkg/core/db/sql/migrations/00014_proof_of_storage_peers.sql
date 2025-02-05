-- +migrate Up

drop table if exists pos_challenges;
drop type if exists challenge_status;
drop index if exists idx_pos_challenges_block_height;

-- This table should be excluded from the abci app state.
-- Storage nodes will track peers for proof of storage challenges and use this to reject spurious provers.
create table storage_proof_peers(
  id serial primary key,
  block_height bigint not null unique,
  prover_addresses text[] not null
);

create index idx_storage_proof_peers_block_height on storage_proof_peers(block_height desc);

-- +migrate Down
drop table if exists storage_proof_peers;
drop index if exists idx_storage_proof_peers_block_height;
