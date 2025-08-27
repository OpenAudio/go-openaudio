-- +migrate Up
create type validator_event as enum ('registered', 'deregistered');

create table if not exists validator_history (
    rowid serial primary key,
    endpoint text not null,
    eth_address text not null,
    comet_address text not null,
    sp_id bigint not null,
    service_type text not null,
    event_type validator_event not null,
    event_time timestamp not null,
    event_block bigint not null
);

create index if not exists idx_validator_history_event_time on validator_history(event_time);

-- +migrate Down
drop table if exists validator_history;
drop type if exists validator_event;
