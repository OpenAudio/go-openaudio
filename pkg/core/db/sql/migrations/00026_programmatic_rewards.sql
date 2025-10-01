-- +migrate Up
create table if not exists core_rewards (
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

create index if not exists idx_core_rewards_address on core_rewards (address);
create index if not exists idx_core_rewards_reward_id on core_rewards (reward_id);
create index if not exists idx_core_rewards_block_height on core_rewards (block_height);
create index if not exists idx_core_rewards_sender on core_rewards (sender);
create index if not exists idx_core_rewards_claim_authorities on core_rewards using gin (claim_authorities);

-- +migrate Down
drop index if exists idx_core_rewards_address;
drop index if exists idx_core_rewards_reward_id;
drop index if exists idx_core_rewards_block_height;
drop index if exists idx_core_rewards_sender;
drop index if exists idx_core_rewards_claim_authorities;

drop table if exists core_rewards;
