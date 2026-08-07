-- +migrate Up
-- Consensus-side content authorization: which users may claim a given cid on a
-- track. Maintained by FinalizeBlock inside the block's transaction, alongside
-- core_auth_* (00037), so every validator holds an identical copy.
--
-- Rows come from a validator attesting an upload, or from the genesis migration
-- seeding an existing track to its owner.
--
-- Keyed on the user the content belongs to, not the wallet that uploaded it.
-- A developer app is one wallet shared by every user who granted it, so
-- authorizing whoever can act for the uploading wallet would let any of an
-- app's users claim any other's uploads. The user id comes from the uploader's
-- own signed request.
--
-- A cid may be claimed by more than one user: possession is the entitlement, so
-- two uploaders who each genuinely hold the same bytes should each be able to
-- claim them. Restricting to one would also lock out the ~115k legacy track
-- rows whose cid is shared across owners.
--
-- Only the authorization question is answered here — the attesting validator,
-- the uploading wallet and the height all live in the attestation transaction
-- already, and duplicating them would add columns no query reads.
--
-- tx_hash is the exception. The sibling core_auth_* tables carry no provenance,
-- but "why may this user claim this cid" is a dispute question, and without a
-- pointer answering it means scanning the whole transaction log for an
-- attestation naming that cid. Empty for migration-seeded rows: their
-- provenance is the genesis block range, and the claim is re-derivable from the
-- track itself.
create table if not exists core_auth_cids (
    cid text not null,
    user_id bigint not null,
    tx_hash text not null default '',
    primary key (cid, user_id)
);

-- +migrate Down
drop table if exists core_auth_cids;
