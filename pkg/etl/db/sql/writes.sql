-- name: InsertAddress :exec
insert into etl_addresses (address, pub_key, first_seen_block_height, created_at)
values ($1, $2, $3, $4)
on conflict do nothing;

-- name: InsertTransaction :exec
insert into etl_transactions (tx_hash, block_height, tx_index, tx_type, address, created_at)
values ($1, $2, $3, $4, $5, $6);

-- name: InsertBlock :exec
insert into etl_blocks (proposer_address, block_height, block_time)
values ($1, $2, $3);

-- name: InsertPlay :exec
insert into etl_plays (user_id, track_id, city, region, country, played_at, block_height, tx_hash, listened_at, recorded_at)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: InsertPlays :exec
insert into etl_plays (user_id, track_id, city, region, country, played_at, block_height, tx_hash, listened_at, recorded_at)
select unnest($1::text[]), unnest($2::text[]), unnest($3::text[]), unnest($4::text[]), unnest($5::text[]), unnest($6::timestamp[]), unnest($7::bigint[]), unnest($8::text[]), unnest($9::timestamp[]), unnest($10::timestamp[]);

-- name: InsertManageEntity :exec
insert into etl_manage_entities (address, entity_type, entity_id, action, metadata, signature, signer, nonce, block_height, tx_hash, created_at)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: InsertValidatorRegistration :exec
insert into etl_validator_registrations (address, endpoint, comet_address, eth_block, node_type, spid, comet_pubkey, voting_power, block_height, tx_hash)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: InsertValidatorDeregistration :exec
insert into etl_validator_deregistrations (comet_address, comet_pubkey, block_height, tx_hash)
values ($1, $2, $3, $4);

-- name: InsertSlaRollup :exec
insert into etl_sla_rollups (block_start, block_end, block_height, validator_count, block_quota, bps, tps, tx_hash, created_at)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: InsertSlaRollupReturningId :one
insert into etl_sla_rollups (block_start, block_end, block_height, validator_count, block_quota, bps, tps, tx_hash, created_at)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
returning id;

-- name: InsertSlaNodeReport :exec
insert into etl_sla_node_reports (sla_rollup_id, address, num_blocks_proposed, challenges_received, challenges_failed, block_height, tx_hash, created_at)
values ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: InsertValidatorMisbehaviorDeregistration :exec
insert into etl_validator_misbehavior_deregistrations (comet_address, pub_key, block_height, tx_hash, created_at)
values ($1, $2, $3, $4, $5);

-- name: InsertStorageProof :exec
insert into etl_storage_proofs (height, address, prover_addresses, cid, proof_signature, proof, status, block_height, tx_hash, created_at)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: InsertStorageProofVerification :exec
insert into etl_storage_proof_verifications (height, proof, block_height, tx_hash, created_at)
values ($1, $2, $3, $4, $5);

-- name: RegisterValidator :exec
insert into etl_validators (address, endpoint, comet_address, node_type, spid, voting_power, status, registered_at, deregistered_at, created_at, updated_at)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) 
on conflict (endpoint) do nothing;

-- name: DeregisterValidator :exec
update etl_validators set deregistered_at = $1, updated_at = $2, status = $3 where comet_address = $4;
