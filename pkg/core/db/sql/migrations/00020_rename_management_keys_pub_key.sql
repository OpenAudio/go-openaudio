-- +migrate Up
ALTER TABLE management_keys RENAME COLUMN pub_key TO address;

-- +migrate Down
ALTER TABLE management_keys RENAME COLUMN address TO pub_key; 
