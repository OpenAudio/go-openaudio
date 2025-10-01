-- +migrate Up
create table if not exists core_uploads(
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

-- Index for looking up by original CID
create index if not exists idx_core_uploads_cid on core_uploads(cid);
-- Index for looking up by transcoded CID
create index if not exists idx_core_uploads_transcoded_cid on core_uploads(transcoded_cid);

-- +migrate Down
drop table if exists core_uploads;
