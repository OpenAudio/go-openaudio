-- +migrate Up
-- Index for requireRegisteredSignature cidstream lookups: cid -> track_id
create index if not exists idx_sound_recordings_cid on sound_recordings(cid);

-- Composite index for management_keys auth check: (track_id, address)
create index if not exists idx_management_keys_track_id_address on management_keys(track_id, address);

-- +migrate Down
drop index if exists idx_sound_recordings_cid;
drop index if exists idx_management_keys_track_id_address;
