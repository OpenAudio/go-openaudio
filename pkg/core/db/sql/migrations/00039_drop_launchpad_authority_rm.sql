-- +migrate Up
-- launchpad_authority_rm (00034) mapped launchpad-derived per-mint claim
-- authorities to their Solana reward manager pubkeys. It had two readers: the
-- 00034 backfill, which resolved each pre-pool core_rewards row to a real RM,
-- and finalizeLegacyCreateReward, which replayed audius-mainnet-alpha-beta's
-- pre-pool reward bytes into pools at block-sync time.
--
-- Both are gone. The backfill ran once, and audius-mainnet-beta was written by
-- genesis-writer from table state, so it carries only the pool-shaped reward
-- transactions and the wire-compat layer has been removed.
drop table if exists launchpad_authority_rm;

-- +migrate Down
-- The 72 rows are not restored: the only consumer that could read them no
-- longer exists in any binary that runs this schema. Recreate the table empty
-- so 00034's Down still finds it.
create table if not exists launchpad_authority_rm (
    authority              text primary key,
    rewards_manager_pubkey text not null,
    created_at             timestamp with time zone default now()
);
