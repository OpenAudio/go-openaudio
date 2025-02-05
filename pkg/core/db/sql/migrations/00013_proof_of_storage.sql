-- +migrate Up

-- unresolved: no majority proof has been verified (yet), results are unknown/inconclusive
-- complete: a majority proof has been verified and results are conclusive
create type challenge_status as enum ('unresolved', 'complete');
create table pos_challenges(
  id serial primary key,
  block_height bigint not null unique,
  -- Storage nodes will add prover_addresses asynchronously and use it to reject spurious provers.
  prover_addresses text[],
  status challenge_status not null default 'unresolved'
);

-- unresolved: no majority proof has been verified (yet), results are unknown/inconclusive
-- pass: node passed the challenge
-- fail: node failed the challenge
create type proof_status as enum ('unresolved', 'pass', 'fail');
create table storage_proofs(
  id serial primary key,
  block_height bigint not null,
  address text not null,
  -- What this prover believed the cid to be (useful for debugging).
  cid text,
  proof_signature text,
  proof text,
  -- What this prover believed the rendezvous replicas should have been.
  prover_addresses text[] not null default '{}',
  status proof_status not null default 'unresolved',
  unique (address, block_height)
);

create index idx_pos_challenges_block_height on pos_challenges(block_height desc);
create index idx_storage_proofs_block_height on storage_proofs(block_height desc);

-- +migrate Down
drop table if exists pos_challenges;
drop table if exists storage_proofs;
drop type if exists challenge_status;
drop type if exists proof_status;
drop index if exists idx_pos_challenges_block_height;
drop index if exists idx_storage_proofs_block_height;
