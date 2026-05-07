-- +migrate Up

-- launchpad_authority_rm was introduced in 00033 to map per-mint
-- claim authorities (lowercased eth hex) to their Solana reward
-- manager pubkeys. It served two purposes: (1) the 00033 backfill
-- block resolved each existing core_rewards row's RM via this
-- mapping; (2) the wire-compat layer (finalizeLegacyCreateReward)
-- queried it at block-sync replay time to converge with the
-- migration's apphash.
--
-- Both consumers are gone in this PR — the wire-compat layer is
-- removed entirely (the network is being restarted with genesis
-- replay, so no historical legacy bytes exist on chain) and the
-- backfill block in 00033 has already run on existing chains.
-- The mapping table is now pure dead weight; drop it.
drop table if exists launchpad_authority_rm;

-- +migrate Down

-- Restoring the table is intentional non-functionality: the Up
-- direction permanently removes data that we don't want to
-- regenerate. If a downgrade is genuinely needed, the operator must
-- re-run 00033's launchpad_authority_rm INSERT VALUES manually
-- against an empty table created here.
create table if not exists launchpad_authority_rm (
    authority              text primary key,
    rewards_manager_pubkey text not null,
    created_at             timestamp with time zone default now()
);
