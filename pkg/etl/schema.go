package etl

import "github.com/OpenAudio/go-openaudio/etl/processors"

// Re-export transaction type constants from processors for use in indexer.
// The canonical definitions are in processors so processors need not import etl.
var (
	TxTypePlay                       = processors.TxTypePlay
	TxTypeManageEntity               = processors.TxTypeManageEntity
	TxTypeValidatorRegistration      = processors.TxTypeValidatorRegistration
	TxTypeValidatorDeregistration    = processors.TxTypeValidatorDeregistration
	TxTypeValidatorRegistrationLegacy = processors.TxTypeValidatorRegistrationLegacy
	TxTypeSlaRollup                  = processors.TxTypeSlaRollup
	TxTypeValidatorMisbehaviorDereg  = processors.TxTypeValidatorMisbehaviorDereg
	TxTypeStorageProof               = processors.TxTypeStorageProof
	TxTypeStorageProofVerification   = processors.TxTypeStorageProofVerification
	TxTypeRelease                    = processors.TxTypeRelease
)

// Schema documentation. Source of truth: pkg/etl/db/sql/migrations/0001_etl_tables.up.sql
//
// Tables:
//
//   - etl_blocks: proposer_address, block_height, block_time
//   - etl_transactions: tx_hash, block_height, tx_index, tx_type, address
//   - etl_plays: user_id, track_id, city, region, country, played_at, block_height, tx_hash
//   - etl_addresses: address, pub_key, first_seen_block_height
//   - etl_manage_entities: address, entity_type, entity_id, action, metadata, signature, signer, nonce
//   - etl_validator_registrations: address, endpoint, comet_address, eth_block, node_type, spid, comet_pubkey, voting_power
//   - etl_validator_deregistrations: comet_address, comet_pubkey
//   - etl_validators: address, endpoint, comet_address, node_type, spid, voting_power, status, registered_at, deregistered_at
//   - etl_validator_misbehavior_deregistrations: comet_address, pub_key
//   - etl_sla_rollups: block_start, block_end, validator_count, block_quota, bps, tps
//   - etl_sla_node_reports: sla_rollup_id, address, num_blocks_proposed, challenges_received, challenges_failed
//   - etl_storage_proofs: height, address, prover_addresses, cid, proof_signature, status
//   - etl_storage_proof_verifications: height, proof
const _schemaDoc = ""
