-- +migrate Up
-- Consensus-side content authorization state: which wallet is entitled to
-- claim a given cid on a track. Maintained by FinalizeBlock inside the block's
-- transaction, alongside core_auth_* (00037), so every validator holds an
-- identical copy.
--
-- A row is created either by a validator attesting that a wallet uploaded
-- those bytes to it (FileUpload), or by the genesis migration replaying a
-- track that already exists (seeded to the track owner, since the legacy
-- source data is the authority on who owns what).
--
-- First attestation wins: cid is the primary key, and a conflicting insert
-- naming a different uploader is skipped rather than overwriting. Without
-- that, a single malicious registered validator could attest that a wallet it
-- controls uploaded any cid it happens to hold bytes for — it mirrors other
-- nodes' blobs, so that is most of the network's content — and re-open the
-- claim bypass this table exists to close. Losing a race is recoverable; a
-- silent ownership overwrite is not.
create table if not exists core_auth_cids (
    -- content id being claimed (original, transcoded, or preview)
    cid text primary key,
    -- lowercased wallet entitled to assert this cid on a track
    uploader_address text not null,
    -- lowercased validator wallet that attested; empty for migration-seeded rows
    attested_by text not null default '',
    -- height the row was recorded at, for debugging and conflict forensics
    block_height bigint not null default 0
);

-- Lookups are by cid (primary key) on the enforcement path. This index serves
-- the reverse question — what has this wallet uploaded — for operators.
create index if not exists idx_core_auth_cids_uploader on core_auth_cids(uploader_address);

-- +migrate Down
drop table if exists core_auth_cids;
