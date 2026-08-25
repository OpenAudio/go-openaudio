-- +migrate Up
-- Give every chain-derived table the core_ prefix.
--
-- "core_" already means something specific in this schema: the table is derived
-- from block execution, must therefore be byte-identical on every validator,
-- and must ship in the state-sync snapshot dumped by createPgDump in
-- pkg/core/server/state_sync.go. These ten tables have always met that
-- definition; they just predate the convention and never got the prefix.
--
-- The gap is not cosmetic. The snapshot table list is hand-maintained and the
-- schema is not, so nothing connects "a migration added a table" to "the table
-- ships in snapshots". That has already gone wrong twice:
--
--   * 00028_fix_missing_tables_from_state_sync.sql exists for no other reason
--     than to recreate core_ern/core_mead/core_pie/core_rewards/core_uploads on
--     nodes that state-synced while those tables were missing from the list.
--   * core_auth_cids (00038) was missed the same way and is being added to the
--     list separately.
--
-- Once the prefix is exhaustive, a guard test can assert the invariant by
-- prefix -- every core_* table in the schema is in the snapshot list, or is
-- explicitly exempted with a reason -- instead of relying on whoever adds the
-- next table to also remember the list.
--
-- RENAME TO is a catalog-only operation: it rewrites one row in pg_class and
-- moves no data. That matters here because these run at startup against
-- production databases in the hundreds of gigabytes, where anything that
-- rewrote a heap would mean a long outage.
--
-- Indexes, constraints and sequences deliberately keep their existing names.
-- RENAME TO does not touch them, and that is fine: nothing in the codebase
-- references any of them by name -- sqlc queries name tables and columns only,
-- and there is no ON CONFLICT ON CONSTRAINT anywhere -- so renaming them would
-- buy no safety. Doing it correctly would also mean renaming the implicit names
-- PostgreSQL generated, several of which appear nowhere in these migration
-- files and would have to be recovered from a live catalog rather than read
-- here: every *_pkey, and sla_node_reports_address_sla_rollup_id_key from the
-- unnamed `unique (address, sla_rollup_id)` in 00006. A half-renamed set reads
-- as intentional and hides whatever was missed, which is worse than a uniformly
-- stale one.
--
-- So, concretely, after this migration a fresh database has:
--
--   core_sla_rollups       idx_time, idx_sla_rollups_block_end, sla_rollups_pkey
--   core_sla_node_reports  idx_sla_node_reports_rollup_address,
--                          sla_node_reports_address_sla_rollup_id_key,
--                          sla_node_reports_pkey
--   core_sound_recordings  idx_sound_recordings_cid, idx_sound_recordings_track_id,
--                          sound_recordings_pkey
--
-- If you are looking at pg_indexes and wondering why core_sla_rollups has an
-- index called idx_time, the rename was not left unfinished -- index,
-- constraint and sequence names were intentionally out of scope.

alter table access_keys rename to core_access_keys;
alter table launchpad_authority_rm rename to core_launchpad_authority_rm;
alter table management_keys rename to core_management_keys;
alter table sla_node_reports rename to core_sla_node_reports;
alter table sla_rollups rename to core_sla_rollups;
alter table sound_recordings rename to core_sound_recordings;
alter table storage_proof_peers rename to core_storage_proof_peers;
alter table storage_proofs rename to core_storage_proofs;
alter table track_releases rename to core_track_releases;
alter table validator_history rename to core_validator_history;

-- +migrate Down
-- Note that running Down is not sufficient on its own to move a node back to a
-- pre-00039 binary: the old binary also has to be the one running afterwards,
-- since it queries the unprefixed names. See the PR body for the full
-- cross-version state-sync analysis.
alter table core_access_keys rename to access_keys;
alter table core_launchpad_authority_rm rename to launchpad_authority_rm;
alter table core_management_keys rename to management_keys;
alter table core_sla_node_reports rename to sla_node_reports;
alter table core_sla_rollups rename to sla_rollups;
alter table core_sound_recordings rename to sound_recordings;
alter table core_storage_proof_peers rename to storage_proof_peers;
alter table core_storage_proofs rename to storage_proofs;
alter table core_track_releases rename to track_releases;
alter table core_validator_history rename to validator_history;
