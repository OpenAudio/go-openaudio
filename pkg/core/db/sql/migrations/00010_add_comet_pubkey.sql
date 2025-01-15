-- +migrate Up
alter table core_validators
add column comet_pub_key text not null;

-- +migrate Down
alter table core_validators
drop column comet_pub_key;
