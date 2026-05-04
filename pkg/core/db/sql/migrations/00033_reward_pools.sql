-- +migrate Up

-- Reward pools group the eth addresses authorized to attest for rewards
-- under a specific Solana reward manager. The pool is identified BY the
-- reward manager pubkey (32-byte base58) — there is no separate "pool
-- address" concept. PR2's CreateRewardPool / SetRewardPoolAuthorities txs
-- mutate this table; PR3's sender-attestation gate consults it to decide
-- whether to sign add/delete attestations for a given (RM, eth address)
-- pair.
create table if not exists core_reward_pools (
    rewards_manager_pubkey text primary key,
    authorities            text[] not null default '{}',
    created_at             timestamp with time zone default now(),
    updated_at             timestamp with time zone default now()
);

create index if not exists idx_core_reward_pools_authorities on core_reward_pools using gin (authorities);

-- launchpad_authority_rm maps a launchpad-derived per-mint claim authority
-- (lowercased eth hex address) to the Solana reward manager state account
-- that mint's rewards live under. The values are produced by running
-- DeriveEthAddressForMint(domain="claimAuthority", secret, mint) for each
-- known launchpad mint and recording its RM. This table serves two
-- purposes:
--
--   1. The backfill block below uses it to find each existing reward row's
--      RM (by looking for any one of its claim_authorities that matches a
--      known launchpad authority).
--   2. PR2's wire-compat layer (finalizeLegacyCreateReward) queries it at
--      block-sync replay time to produce the same RM-bound state the
--      migration produces, so historical replay and the migration agree
--      on the resulting apphash.
--
-- The table must remain populated and queryable as long as any legacy-
-- format CreateReward txs may still be replayed during block sync. Future
-- launchpad mints can be added via additional migrations as they're
-- registered.
create table if not exists launchpad_authority_rm (
    authority              text primary key,
    rewards_manager_pubkey text not null,
    created_at             timestamp with time zone default now()
);

insert into launchpad_authority_rm (authority, rewards_manager_pubkey) values
    ('0x0c5b590a071a377c49a154639fd4343d147bcafe', 'urGNtpg4ppvLKH5Yvkmi1dixbTsokJdAdbzGs9iSSPy'),
    ('0x103cf68f8d352ba3dbee8b56236befaa1e1429aa', 'BWC8rj4am5izbJXCpwyEfTxAo2StWGiwjBfSzAzKt4Gi'),
    ('0x111d7c8cb8c46f743a7796a7f9c0f2fb08076f6f', '7hXopQnXfv8tRf8aVXvGauAHyUPcNKAT4gp49ufhxk36'),
    ('0x1165e5f035ce5eeaed37c6ad190b58903bac6175', 'Di77DoUUbe8QDq6XU3jptxMLnT8S5kj8JZ3qGYRTHNKh'),
    ('0x14035c457a724b83da6c88b7dca0d283d2789808', 'dWwazU2QUih1pDnuGWXfJnVG6uLfvogqnFknvrxju92'),
    ('0x14637fad403350a1fdbd6d21cc7658ae46d7bbb0', '5z5TV5WhbUyhJDkhmeKsJLNgreVcNq3kNh3Kwd2mGHEE'),
    ('0x16ac1f19e207794ff1462b2ffdad742b3b3c0ce7', 'Bvr2HYVpKThU4nCFhpDy7Y6XLBMZUuQHK1KRbAGnCiQt'),
    ('0x1a17cf24fa74847c4c2e4e14d9fa5d55347ea829', 'FGrk4MQGFyjsgnCBCoCpRgMubo6SqSwvHYiEHuDnXrcE'),
    ('0x1be07e9b5af6cad186f8e3771d97e11b3af27dbe', 'FDeFBV6XntX2LkkYFmBSyqhwf8w7MzvND3kcJWYD4gVa'),
    ('0x1edf70aaf55952a8d9d75a550e0f7eef6593c0b9', 'GBDpJArbu7Mes5FnXYgMNAaUdayCAGRzpJrThaKr9dsS'),
    ('0x212fbc84a052d73b9dbceae3c8de29597db64d0f', 'E7fZSM55CzM7gD9LcnN5jEakKJLcHUwyNQZc9RL2RgTT'),
    ('0x21adafbd269728c08f2ee6d3a3a43455b7e09ad7', '7Lra25qHo7yJWTQjwAGcyK3PvHXLfKpqNZkpGX76dzkK'),
    ('0x27ca8265e6d9f0a95657fa7d36964b10d826fa6d', 'BtU6seJeyf5b68mSA2iFH1wdipxNHWSuU9JGKhAYVtry'),
    ('0x29063c045b421d064993bde6e98646b47998c4f7', 'CDZ6nAr8md5xz2Wvb3LdnLya6yjxpqcLTMZrb8zYMdcM'),
    ('0x36d1b69132f70558dc4838d5ae87cca21c562f1f', '2QJhBYiEznpjgSSsezcccpfkYdngPik4fvQzxHMucm9L'),
    ('0x3b3c30eb5dd9bb6f9b92a14448474242513299b2', 'GXX15br7UgnkhUskBtfTn26SsWbtjijofxGH5Be1LKqP'),
    ('0x3e472953a85573bf25c657772a5f05624de5d9f1', 'FhHiVewd3dM63nJ6h3cZFdvMcyFdVJURhHUfYvvXDpQm'),
    ('0x48f31a62c79ead300514cb890e68da88acc018e8', '3zaWKMBCR2mDoKCGjtyB4Y24xHQa1XdT2tGXuRThKg8U'),
    ('0x4b797d0822d595de3e3ac3f2747bd5fdc2dfe96d', 'AvGcJBQpiWTvFFXW7GHYZXHpuNxjvGpZ8ezpjQXf2KN9'),
    ('0x4e870de81a26d7e84dee34e8678b5958fe8730d1', '8CtDHsjPUpnGyNSAQpwGqPm46Np6jJWxRpzXH2277RX3'),
    ('0x4fff490d410d5f412392bbad9e5b2b18742eea6c', '8DY6uaCyzmSXNneycc45tS1TxUTPjuYiFQYtsjWsASSN'),
    ('0x503d34f7615b2a12651a54286f0d27d732647993', 'pRS2Gkt9YNoU1G9HAXeRruKGN5qRUQsg5pVxbtAuPmV'),
    ('0x50dec0d97ee06a32c81651b8430ff424d6c58dc6', '4Aa5UrKqeUPrUVHRjiGAF6o1V3u17v2rQmERNEXLNkZ9'),
    ('0x550d7cb8e314828c29bd48ce8e732e5799efe08d', 'GGrpfPa8gXPQ3FEusXhsPQBnWeNqb7Z1cfSD6gY3zunQ'),
    ('0x5ad1a050b693f59eb64e4ac6d34bf30c3880cd24', 'BNJMsdNJN4NRJcisFtPbWF799eKa19YafLs6oDZAYDGm'),
    ('0x5c78fe2d276450237d569bc4afa763bf9ccaf74f', 'GCrhftvMubAEQrnCcTUxLHxsCbnGr7wVdLtwjj1SkWQN'),
    ('0x5f7f7dacbc1f64e28d465cb2644592963771d618', 'CBaP4ncJYxv2wmjT8bxtpuJ41wJU8SkbMKuaP4PP9S8C'),
    ('0x66a9d306336e227a47a3486669bba023b141192d', '65EKqnYE8Rom3DnLGP7XbXjyNY8xXMfCnpjMZW14tsvV'),
    ('0x6702d3bff4b3736cf72f9f64db4169a5853dbc9c', '83ENNoux1oKhMndc2Uw6BqwWTPHPavHWQhG292bZun3N'),
    ('0x6b13053a2b4c7fc71d60d58dd55a97b34b245a80', '2WdvdUbEEY1XRYVQmrHFipTWg8Jy42nE4e7xFK3Q6gjo'),
    ('0x6c6da83a8237ee4dc4ecfea880c17c4ed6d7a2c8', 'F3xcZ7jQFWBgp2kMjp9fHSkCehRWzuVND6n1mCu1oyjH'),
    ('0x832d886079387f3ab825e978bfc682f6c158a3e8', '8FkZU5VooBFXqb8GA36kgTyHZ5J1ABqi13bBKzkExvBz'),
    ('0x85f54509c6075ed7efe6889a85b713cb68ce56cf', '6UuCBYGutteDTYAhd1W5PpvvSAg4LEWiJYQnWVU79bER'),
    ('0x868d17a61edafd0b9d96032cd44570a95ff6a04f', '86jE4ubrbFGwdwTv6iN2FZpQm2EiHXru4teowrYpu8Tn'),
    ('0x86a60f5b2b3f29a42c80fec03818bd329655187f', '4kmzFHSSrUxeTkfRrYmKv96o7guqi7LPBaZrzqvKQYL5'),
    ('0x8719de0df16a3e0889d8509b351dafabee4ea294', 'GaDBQ27F7EmNHumchE6Pjr43frckBuxsUUJjN45uAajQ'),
    ('0x8ad3ca46321b18e06b35d4c8e76d18d976de7727', 'GQxHiQFfeby9wfyUw5Bw1Q1xPB7ZqwgKjB36PtHhKstV'),
    ('0x8f4458d8f0ae1b3f83e84be178fd1c4f3fc36bd4', 'CAQm1eMbUn3j5qhFWT4FmnDzwuNddy1supzR5A85VVfW'),
    ('0x90aaffd8c2bbb0b68e99b90319c3c7bb43d33eb2', 'mY3V2Y2Trjsaa3qHrrL7s3aNWEG6getJUdpXvf71uqe'),
    ('0x9293d36202216e47afb9d7762f05ad52c59403e4', 'AdDEp4rJe7RAFRg14uaFMUmPfLHJ6oJeSe49aXSwPVgZ'),
    ('0x9d7ba5f04c7dc2993ff2ccdf19e29ad741036e90', '78XW1jt6T5mXTBocoDuTqB4RAM3mAKnYGSYWDeXY135s'),
    ('0x9f3ebdf813c04ef8096326dd134270b376525e1a', 'fWcM3QvikFoZGKQ9eJ8yZ4Q7SVcHydnGMoR7fdrtza9'),
    ('0xa1ef8ef283501d456b76757b32ea345cdcb4224f', '8u3ejuKz7uywPqdz5fS8cB4ppXvAZ3WiLeVKdiFWoK2r'),
    ('0xa29dff72f7aecb95d1414f7206eb69820ec86cc7', '2hcSdCmtCfgNhjBELh6a9GJ2WSbbU8LMMonqFLZTAp67'),
    ('0xa85281a327c0848a619659391eb88dd3702fcf41', '781STZvPpSTaDNcB35tbAWk2fwbTpqaRNRpSQpun7C3X'),
    ('0xaa49c2afad744ac3595baed0d1f9c7876720c9df', 'EUqRiCAgj7QnP176ThaGfrGz3r2VxxNyXuAPU5SPhMJS'),
    ('0xad6b9555369a438fb4698c46dfad245ef97b270e', '8jrWskYgwYPEZHSUCKnQmdq4hGKgAt9gcZeXtQCsRnJs'),
    ('0xb3f3f879c7c0cb0a4af1fcddb6557fc2b3963bc9', '3Z4GXG84TjmvnV8h16qraFmWeEBv2eQd9quc39UhwiAe'),
    ('0xb61525df350dbfc6a2ac82412f6ece0cc4dbd7ff', 'CFawCKFwxqLejDdoyUMjyBQokX2n37E37oS8HAUogmAM'),
    ('0xbbecbfecf49bb3159c67fa5d254c40307c6fce2b', '86Wqf14Hj3DQMwgpuqQtAApJT3Wyf4wR7wWmdYzfkLWZ'),
    ('0xbc731ab6905ef383b519702dffbe8bb3f9ccf547', 'EgTvyvn9QDsAyXVamM8pzV2YHoVRuWBVmRFD56BaGgkS'),
    ('0xbd00af91f78a651bdb74892f7c7207ffed7ef3dd', 'A3ZhYjqJfBZtJxUGXXruYHSWR5Zf7NaBgteTwEhrKwow'),
    ('0xc402938db359d2644df952697dc3925c62fef99e', 'B4ijMf1byKLmRUV9RZaDCZqs4TTPGEfbEYD67B5M22Z2'),
    ('0xc82c810ae45ba45d5ccdc02273f48e453d51e2f9', '6YAc7rLW8uom3DRnj6wkwvq6qpW9PkAfxBxXpU2wyyph'),
    ('0xc8acdb3049903f6758c434d21b77af2b8af48d9e', 'ELs98dgSh3Kd6ViMM9LFT1rfEbPvm4e6xpQzjK2wVjH6'),
    ('0xce61db54629ef47d0dfb2923660c787ac92fcec1', 'P76iaELTfBg1hKo6hr22scupEMhDsAcayg5AMGpQiCM'),
    ('0xcece39acb112bfbc59fe41606d4475435f1b3676', '4E7B4BGd2bfjutdjkWsMcjgubGkxgf5niNNuKQKhbfpQ'),
    ('0xda1a09589098954b05648419cc4bbea20c6e39ea', '46w1RsTcygL5C6ijfaAztUstPqHuzZXPEB2KCr52cYHf'),
    ('0xdaef8d116025a7053577c95cf793827cbf6d7767', '7ij5BsYE2aL3QG2Mt1ifB3mNZx9ELXBEZbCMX6nLM977'),
    ('0xe325971fe787077e8b6d42e9f353eea06a801680', '31Dq71KBhKEs2nEwytX8RwEC1wBS2pUaQdPVk3zTHsWP'),
    ('0xe9448fb249fb675f6d51483edef063bf64487a3f', 'GqHmpeQkqNW7h8mPUQPsXqy3E5CVy1siMbeHuU9uhNVi'),
    ('0xee878a78a703a67c73d1e794e61bb36fc715eef2', '6TW5NzdMF7ps4zaDo6ogCV1iUHde4VdA2ZyNtJKLwSLp'),
    ('0xf0ed91252a509ff2d24f0dd6e22c0e985f636334', 'BrY2VSnagcjsr6DND1FpcfpYiJ8PrBdheuPRggKVfZUN'),
    ('0xf174de7442c805c3900ce4140a81f329c5a7f5bc', '3G9yANcxNcFobZgZbP3TyQuF4zqC3hqeqEi1LDcJjJgo'),
    ('0xf3baf2379bb060fa63f620576fbb977ef5fa4e51', '8dZLVViKogvR8nPFVLcK64r32WG5GKZojn2BTvJ2LHHL'),
    ('0xfa29072ea2933a8ece9842d19480184ef9583ba8', '8ThSmVGV6NW36JDuVgNCgYuh6b9QycaQCmRT18boS4aR'),
    ('0xfe3d5e7b8bab7599d63f43dcd673f7c27424296e', 'G1YQE4itykWEWSheCzKMKVma7D7AYxrQ7UY92DvZLUeV'),
    ('0xffbb95b5631c39e860016d23918592489031248c', '454bPX92ayZf6XmuGRjFGKGAr43hYX2T8U3avcd6KNGi')
on conflict (authority) do nothing;

-- Add rewards_manager_pubkey FK to core_rewards. Nullable: rows whose
-- claim_authorities don't include any known launchpad-derived authority
-- (e.g., test fixtures, abandoned rewards) stay NULL after the backfill
-- and have no pool. PR3's sender-attestation gate refuses to operate on
-- NULL-RM rewards (no pool, no authority data).
alter table core_rewards add column if not exists rewards_manager_pubkey text;
create index if not exists idx_core_rewards_rewards_manager_pubkey on core_rewards (rewards_manager_pubkey);

-- Backfill step 1: insert one core_reward_pools row per (RM, canonical
-- authorities) pair seen in core_rewards. The pool's RM is determined by
-- finding any one of the row's claim_authorities that matches a launchpad
-- authority — so each unique authority set becomes a real RM-bound pool,
-- not a synthetic mig_<md5> identifier. The pool's authorities array is
-- the canonical (trim, lower, dedup, sort) form of the row's full
-- claim_authorities, preserving the partner-signer / leaked-key entries
-- as the pool's *initial* authorities. PR2's SetRewardPoolAuthorities is
-- the rotation surface that drains them out.
-- Per-RM authority union: for each RM, take the UNION of authorities
-- across every reward whose claim_authorities contain ANY of that RM's
-- launchpad-derived keys. This guards against the case where two
-- rewards under the same mint were created with slightly different
-- authority sets (e.g., one had an additional debug key) — a naive
-- "ON CONFLICT DO NOTHING" insert would keep an arbitrary
-- first-inserted set and silently drop authorities present on other
-- rewards. The union preserves all of them as the pool's initial set;
-- SetRewardPoolAuthorities is the rotation surface that drains them.
insert into core_reward_pools (rewards_manager_pubkey, authorities)
select
    rm.rewards_manager_pubkey,
    array(
        select distinct lower(trim(a))
        from core_rewards r,
             unnest(r.claim_authorities) as a
        where r.claim_authorities is not null
          and array_length(r.claim_authorities, 1) > 0
          and a is not null
          and trim(a) <> ''
          and exists (
              select 1
              from unnest(r.claim_authorities) as r_auth
              where lower(trim(r_auth)) = rm.authority
          )
        order by 1
    ) as authorities
from launchpad_authority_rm rm
where exists (
    select 1
    from core_rewards r,
         unnest(r.claim_authorities) as r_auth
    where r.claim_authorities is not null
      and array_length(r.claim_authorities, 1) > 0
      and lower(trim(r_auth)) = rm.authority
)
on conflict (rewards_manager_pubkey) do update set
    authorities = excluded.authorities,
    updated_at = now();

-- Backfill step 2: point each core_rewards row at its pool. We pick the RM
-- from any launchpad-derived authority found among the row's
-- claim_authorities. In practice each row should contain exactly one
-- launchpad authority (the per-mint key for the coin the reward was issued
-- under), so the lateral lookup yields a unique RM; rows with no matching
-- authority are left with NULL rewards_manager_pubkey.
update core_rewards r
set rewards_manager_pubkey = mapped.rewards_manager_pubkey
from (
    select
        r2.address as reward_address,
        rm.rewards_manager_pubkey
    from core_rewards r2
    cross join lateral (
        select rm.rewards_manager_pubkey
        from launchpad_authority_rm rm
        where rm.authority = any (
            select lower(trim(a)) from unnest(r2.claim_authorities) a
            where a is not null and trim(a) <> ''
        )
        order by rm.rewards_manager_pubkey, rm.authority
        limit 1
    ) rm
    where r2.claim_authorities is not null
      and array_length(r2.claim_authorities, 1) > 0
) mapped
where r.address = mapped.reward_address;

alter table core_rewards
    add constraint fk_core_rewards_rewards_manager_pubkey
    foreign key (rewards_manager_pubkey) references core_reward_pools(rewards_manager_pubkey)
    on delete cascade;

-- The row's rewards_manager_pubkey now carries the authorization signal;
-- the inline claim_authorities column and its gin index are redundant.
drop index if exists idx_core_rewards_claim_authorities;
alter table core_rewards drop column if exists claim_authorities;

-- +migrate Down

-- Restore the column (backfilled from the pool's authorities) before tearing
-- down the FK so existing data is preserved on a downgrade.
--
-- KNOWN LIMITATION: rewards whose claim_authorities did not include any
-- launchpad-derived authority were left with NULL rewards_manager_pubkey
-- by the Up migration (no pool was created for them). Those rows have no
-- pool to restore from, so the JOIN below leaves their claim_authorities
-- as the column default ('{}'). The original inline claim_authorities are
-- not recoverable on a downgrade. This is acceptable because the Down
-- migration is a best-effort rollback path — production rollback would
-- happen at the chain level, not via SQL Down.
alter table core_rewards add column if not exists claim_authorities text[] default '{}';
update core_rewards r
set claim_authorities = coalesce(p.authorities, '{}'::text[])
from core_reward_pools p
where p.rewards_manager_pubkey = r.rewards_manager_pubkey;
create index if not exists idx_core_rewards_claim_authorities on core_rewards using gin (claim_authorities);

alter table core_rewards drop constraint if exists fk_core_rewards_rewards_manager_pubkey;
drop index if exists idx_core_rewards_rewards_manager_pubkey;
alter table core_rewards drop column if exists rewards_manager_pubkey;

drop index if exists idx_core_reward_pools_authorities;
drop table if exists core_reward_pools;
drop table if exists launchpad_authority_rm;
