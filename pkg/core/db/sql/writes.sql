-- name: UpsertAppState :exec
insert into core_app_state (block_height, app_hash)
values ($1, $2);

-- name: InsertRegisteredNode :exec
insert into core_validators(pub_key, endpoint, eth_address, comet_address, comet_pub_key, eth_block, node_type, sp_id)
values ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: DeleteRegisteredNode :exec
delete from core_validators
where comet_address = $1;

-- name: UpsertSlaRollupReport :exec
with updated as (
    update sla_node_reports 
    set blocks_proposed = blocks_proposed + 1
    where address = $1 and sla_rollup_id is null
    returning *
)
insert into sla_node_reports (address, blocks_proposed, sla_rollup_id)
select $1, 1, null
where not exists (select 1 from updated);

-- name: ClearUncommittedSlaNodeReports :exec
delete from sla_node_reports
where sla_rollup_id is null;

-- name: CommitSlaNodeReport :exec
insert into sla_node_reports (sla_rollup_id, address, blocks_proposed)
values ($1, $2, $3);

-- name: CommitSlaRollup :one
insert into sla_rollups (time, tx_hash, block_start, block_end)
values ($1, $2, $3, $4)
returning id;

-- name: InsertTxStat :exec
insert into core_tx_stats (tx_type, tx_hash, block_height, created_at)
values ($1, $2, $3, $4)
on conflict (tx_hash) do nothing;

-- name: StoreBlock :exec
insert into core_blocks (height, chain_id, hash, proposer, created_at)
values ($1, $2, $3, $4, $5);

-- name: StoreTransaction :exec
insert into core_transactions (block_id, index, tx_hash, transaction, created_at)
values ($1, $2, $3, $4, $5);

-- name: InsertStorageProofPeers :exec
insert into storage_proof_peers (block_height, prover_addresses)
values ($1, $2);

-- name: InsertStorageProof :exec
insert into storage_proofs (block_height, address, cid, proof_signature, prover_addresses)
values ($1, $2, $3, $4, $5);

-- name: UpdateStorageProof :exec
update storage_proofs 
set proof = $1, status = $2
where block_height = $3 and address = $4;

-- name: InsertFailedStorageProof :exec
insert into storage_proofs (block_height, address, status)
values ($1, $2, 'fail');

-- name: InsertEtlTx :exec
insert into core_etl_tx (block_height, tx_index, tx_hash, tx_type, tx_data, created_at)
values ($1, $2, $3, $4, $5, $6)
on conflict (tx_hash) do nothing;

-- name: InsertEtlDuplicate :exec
insert into core_etl_tx_duplicates (tx_hash, table_name, duplicate_type)
values ($1, $2, $3)
on conflict (tx_hash, table_name) do nothing;

-- name: InsertDecodedPlay :exec
with duplicate_check as (
    insert into core_etl_tx_plays (
        tx_hash,
        user_id,
        track_id,
        played_at,
        signature,
        city,
        region,
        country,
        created_at
    ) values (
        $1, $2, $3, $4, $5, $6, $7, $8, $9
    ) on conflict (tx_hash, user_id, track_id) do nothing
    returning tx_hash
)
insert into core_etl_tx_duplicates (tx_hash, table_name, duplicate_type)
select $1, 'core_etl_tx_plays', 'tx_user_track'
where not exists (select 1 from duplicate_check)
on conflict (tx_hash, table_name) do nothing;

-- name: InsertDecodedValidatorRegistration :exec
with duplicate_check as (
    insert into core_etl_tx_validator_registration (
        tx_hash,
        endpoint,
        comet_address,
        eth_block,
        node_type,
        sp_id,
        pub_key,
        power,
        created_at
    ) values (
        $1, $2, $3, $4, $5, $6, $7, $8, $9
    ) on conflict (tx_hash) do nothing
    returning tx_hash
)
insert into core_etl_tx_duplicates (tx_hash, table_name, duplicate_type)
select $1, 'core_etl_tx_validator_registration', 'tx'
where not exists (select 1 from duplicate_check)
on conflict (tx_hash, table_name) do nothing;

-- name: InsertDecodedValidatorDeregistration :exec
with duplicate_check as (
    insert into core_etl_tx_validator_deregistration (
        tx_hash,
        comet_address,
        pub_key,
        created_at
    ) values (
        $1, $2, $3, $4
    ) on conflict (tx_hash) do nothing
    returning tx_hash
)
insert into core_etl_tx_duplicates (tx_hash, table_name, duplicate_type)
select $1, 'core_etl_tx_validator_deregistration', 'tx'
where not exists (select 1 from duplicate_check)
on conflict (tx_hash, table_name) do nothing;

-- name: InsertDecodedSlaRollup :exec
with duplicate_check as (
    insert into core_etl_tx_sla_rollup (
        tx_hash,
        block_start,
        block_end,
        timestamp,
        created_at
    ) values (
        $1, $2, $3, $4, $5
    ) on conflict (tx_hash) do nothing
    returning tx_hash
)
insert into core_etl_tx_duplicates (tx_hash, table_name, duplicate_type)
select $1, 'core_etl_tx_sla_rollup', 'tx'
where not exists (select 1 from duplicate_check)
on conflict (tx_hash, table_name) do nothing;

-- name: InsertDecodedStorageProof :exec
with duplicate_check as (
    insert into core_etl_tx_storage_proof (
        tx_hash,
        height,
        address,
        cid,
        proof_signature,
        prover_addresses,
        created_at
    ) values (
        $1, $2, $3, $4, $5, $6, $7
    ) on conflict (tx_hash) do nothing
    returning tx_hash
)
insert into core_etl_tx_duplicates (tx_hash, table_name, duplicate_type)
select $1, 'core_etl_tx_storage_proof', 'tx'
where not exists (select 1 from duplicate_check)
on conflict (tx_hash, table_name) do nothing;

-- name: InsertDecodedStorageProofVerification :exec
with duplicate_check as (
    insert into core_etl_tx_storage_proof_verification (
        tx_hash,
        height,
        proof,
        created_at
    ) values (
        $1, $2, $3, $4
    ) on conflict (tx_hash) do nothing
    returning tx_hash
)
insert into core_etl_tx_duplicates (tx_hash, table_name, duplicate_type)
select $1, 'core_etl_tx_storage_proof_verification', 'tx'
where not exists (select 1 from duplicate_check)
on conflict (tx_hash, table_name) do nothing;

-- name: InsertDecodedManageEntity :exec
with duplicate_check as (
    insert into core_etl_tx_manage_entity (
        tx_hash,
        user_id,
        entity_type,
        entity_id,
        action,
        metadata,
        signature,
        signer,
        nonce,
        created_at
    ) values (
        $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
    ) on conflict (tx_hash) do nothing
    returning tx_hash
)
insert into core_etl_tx_duplicates (tx_hash, table_name, duplicate_type)
select $1, 'core_etl_tx_manage_entity', 'tx'
where not exists (select 1 from duplicate_check)
on conflict (tx_hash, table_name) do nothing;

-- name: InsertTrackId :exec
insert into track_releases (track_id) values ($1);

-- name: InsertSoundRecording :exec
insert into sound_recordings (sound_recording_id, track_id, cid, encoding_details) 
values ($1, $2, $3, $4);

-- name: InsertAccessKey :exec
insert into access_keys (track_id, pub_key) values ($1, $2);

-- name: InsertManagementKey :exec
insert into management_keys (track_id, address) values ($1, $2);

-- ERN, MEAD, PIE insert queries - using protobuf storage with new schema
-- name: InsertCoreERN :exec
insert into core_ern (
    address,
    tx_hash,
    index,
    sender,
    message_control_type,
    party_addresses,
    resource_addresses,
    release_addresses,
    deal_addresses,
    raw_message,
    raw_acknowledgment,
    block_height
) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: InsertCoreMEAD :exec
insert into core_mead (
    address,
    tx_hash,
    index,
    sender,
    resource_addresses,
    release_addresses,
    raw_message,
    raw_acknowledgment,
    block_height
) values ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: InsertCorePIE :exec
insert into core_pie (
    address,
    tx_hash,
    index,
    sender,
    party_addresses,
    raw_message,
    raw_acknowledgment,
    block_height
) values ($1, $2, $3, $4, $5, $6, $7, $8);
