-- +migrate Up

create table if not exists core_tx_decoded (
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

create table if not exists core_tx_decoded_plays (
    id bigserial primary key,
    tx_hash text not null references core_tx_decoded(tx_hash),
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

create index if not exists core_tx_decoded_block_height_idx on core_tx_decoded(block_height);
create index if not exists core_tx_decoded_tx_hash_idx on core_tx_decoded(tx_hash);
create index if not exists core_tx_decoded_tx_type_idx on core_tx_decoded(tx_type);

create index if not exists core_tx_decoded_plays_city_idx on core_tx_decoded_plays(city);
create index if not exists core_tx_decoded_plays_region_idx on core_tx_decoded_plays(region);
create index if not exists core_tx_decoded_plays_country_idx on core_tx_decoded_plays(country);
create index if not exists core_tx_decoded_plays_played_at_idx on core_tx_decoded_plays(played_at);
create index if not exists core_tx_decoded_plays_user_id_idx on core_tx_decoded_plays(user_id);
create index if not exists core_tx_decoded_plays_track_id_idx on core_tx_decoded_plays(track_id);

-- +migrate Down
drop index if exists core_tx_decoded_plays_track_id_idx;
drop index if exists core_tx_decoded_plays_user_id_idx;
drop index if exists core_tx_decoded_plays_played_at_idx;
drop index if exists core_tx_decoded_plays_country_idx;
drop index if exists core_tx_decoded_plays_region_idx;
drop index if exists core_tx_decoded_plays_city_idx;

drop index if exists core_tx_decoded_tx_type_idx;
drop index if exists core_tx_decoded_tx_hash_idx;
drop index if exists core_tx_decoded_block_height_idx;

drop table if exists core_tx_decoded_plays;
drop table if exists core_tx_decoded;
