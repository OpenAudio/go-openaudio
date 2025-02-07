-- name: GetTx :one
select * from core_transactions where lower(tx_hash) = lower($1) limit 1;

-- name: TotalTxResults :one
select count(tx_hash) from core_transactions;

-- name: GetLatestAppState :one
select block_height, app_hash
from core_app_state
order by block_height desc
limit 1;

-- name: GetAppStateAtHeight :one
select block_height, app_hash
from core_app_state
where block_height = $1
limit 1;

-- name: GetAllRegisteredNodes :many
select *
from core_validators;

-- name: GetAllRegisteredNodesSorted :many
select *
from core_validators
order by comet_address;

-- name: GetNodeByEndpoint :one
select *
from core_validators
where endpoint = $1
limit 1;

-- name: GetNodesByEndpoints :many
select *
from core_validators
where endpoint = any($1::text[]);

-- name: GetRegisteredNodesByType :many
select *
from core_validators
where node_type = $1;

-- name: GetLatestSlaRollup :one
select * from sla_rollups order by time desc limit 1;

-- name: GetRecentRollupsForNode :many
with recent_rollups as (
    select *
    from sla_rollups
    order by time desc
    limit 30
)
select
    rr.id,
    rr.tx_hash,
    rr.block_start,
    rr.block_end,
    rr.time,
    nr.address,
    nr.blocks_proposed
from recent_rollups rr
left join sla_node_reports nr
on rr.id = nr.sla_rollup_id and nr.address = $1
order by rr.time;

-- name: GetRecentRollupsForAllNodes :many
with recent_rollups as (
    select *
    from sla_rollups
    where sla_rollups.id <= $1
    order by time desc
    limit $2
)
select
    rr.id,
    rr.tx_hash,
    rr.block_start,
    rr.block_end,
    rr.time,
    nr.address,
    nr.blocks_proposed
from recent_rollups rr
left join sla_node_reports nr
on rr.id = nr.sla_rollup_id
order by rr.time;

-- name: GetSlaRollupWithTimestamp :one
select * from sla_rollups where time = $1;

-- name: GetSlaRollupWithId :one
select * from sla_rollups where id = $1;


-- name: GetPreviousSlaRollupFromId :one
select * from sla_rollups
where time < (
    select time from sla_rollups sr where sr.id = $1
)
order by time desc
limit 1;

-- name: GetInProgressRollupReports :many
select * from sla_node_reports
where sla_rollup_id is null 
order by address;

-- name: GetRollupReportsForId :many
select * from sla_node_reports
where sla_rollup_id = $1
order by address;

-- name: GetRollupReportForNodeAndId :one
select * from sla_node_reports
where address = $1 and sla_rollup_id = $2;


-- name: GetRegisteredNodeByEthAddress :one
select * from core_validators where eth_address = $1;

-- name: GetRegisteredNodeByCometAddress :one
select * from core_validators where comet_address = $1;

-- name: GetRecentBlocks :many
select * from core_blocks order by created_at desc limit 10;

-- name: GetRecentTxs :many
select * from core_transactions order by created_at desc limit 10;

-- name: TotalBlocks :one
select count(*) from core_blocks;

-- name: TotalTransactions :one
select count(*) from core_transactions;

-- name: TotalTransactionsByType :one
select count(*) from core_tx_stats where tx_type = $1;

-- name: TotalValidators :one
select count(*) from core_validators;

-- name: TxsPerHour :many
select date_trunc('hour', created_at)::timestamp as hour, tx_type, count(*) as tx_count
from core_tx_stats 
where created_at >= now() - interval '1 day'
group by hour, tx_type 
order by hour asc;

-- name: GetBlockTransactions :many
select * from core_transactions where block_id = $1 order by created_at desc;

-- name: GetBlock :one
select * from core_blocks where height = $1;

-- name: GetStorageProofPeers :one
select prover_addresses from storage_proof_peers where block_height = $1;

-- name: GetStorageProof :one
select * from storage_proofs where block_height = $1 and address = $2;

-- name: GetStorageProofs :many
select * from storage_proofs where block_height = $1;

-- name: GetStorageProofRollups :many
select 
    address, 
    count(*) filter (where status = 'fail') as failed_count,
    count(*) as total_count
from storage_proofs 
where block_height >= $1 and block_height <= $2
group by address;

-- name: GetStorageProofsForNodeInRange :many
select * from storage_proofs
where block_height in (
    select block_height
    from storage_proofs sp
    where sp.block_height >= $1 and sp.block_height <= $2 and sp.address = $3 
);
