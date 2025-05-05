-- +migrate Up
ALTER TABLE sound_recordings DROP CONSTRAINT sound_recordings_cid_key;

-- +migrate Down
ALTER TABLE sound_recordings ADD CONSTRAINT sound_recordings_cid_key UNIQUE (cid); 
