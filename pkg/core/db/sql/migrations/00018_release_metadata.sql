-- +migrate Up
create table if not exists track_releases(
  id serial primary key,
  track_id text not null unique
);

create index idx_track_releases_track_id on track_releases(track_id);

create table if not exists sound_recordings(
  id serial primary key,
  sound_recording_id text not null,
  track_id text not null,
  cid text not null unique,
  encoding_details text
);

create index idx_sound_recordings_track_id on sound_recordings(track_id);

create table if not exists access_keys(
  id serial primary key,
  track_id text not null,
  pub_key text not null
);

create index idx_access_keys_track_id on access_keys(track_id);

create table if not exists management_keys(
  id serial primary key,
  track_id text not null,
  pub_key text not null
);

create index idx_management_keys_track_id on management_keys(track_id);
