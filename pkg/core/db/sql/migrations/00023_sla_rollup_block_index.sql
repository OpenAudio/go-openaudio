-- +migrate Up
create index if not exists idx_sla_rollups_block_end on sla_rollups(block_end desc);

-- +migrate Down

drop index if exists idx_time;
drop index if exists idx_sla_rollups_block_end;
