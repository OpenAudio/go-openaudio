-- +migrate Up
create index idx_sla_node_reports_rollup_address on sla_node_reports(sla_rollup_id, address);

-- +migrate Down
drop index if exists idx_sla_node_reports_rollup_address;
