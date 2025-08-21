create table if not exists eth_staked(
    address text primary key,
    total_staked bigint not null
);

alter table if exists eth_registered_endpoints 
add column if not exists registered_at timestamp not null default current_timestamp;
