-- +migrate Up
-- Consensus-side authorization state: a deliberately tiny projection of the
-- authorization-relevant subset of ManageEntity effects, maintained by
-- FinalizeBlock inside the block's transaction so every validator holds an
-- identical copy. It records only who owns which user id, entity, developer
-- app, and which grants are active — enough for consensus to answer "is this
-- signer allowed to act for this user?" without consulting the (non-consensus)
-- ETL. Content semantics (metadata, genres, gating rules) stay out on purpose.

create table if not exists core_auth_users (
    user_id bigint primary key,
    -- lowercased signer wallet that created the account
    wallet text not null,
    -- lowercased handle; nullable because legacy accounts can lack one
    handle_lc text,
    is_deactivated boolean not null default false
);
-- Non-unique on purpose: legacy source data can contain duplicate wallets and
-- handles, and the genesis migration must be able to replay them. Uniqueness
-- for new accounts is a projection/validation rule, not a schema constraint.
create index if not exists idx_core_auth_users_wallet on core_auth_users(wallet);
create index if not exists idx_core_auth_users_handle_lc on core_auth_users(handle_lc);

-- Current grant state per (grantee, grantor) pair. Unlike the ETL's grants
-- table there is no history: consensus only ever asks about the present.
create table if not exists core_auth_grants (
    grantee_address text not null,
    user_id bigint not null,
    -- null = pending user-to-user grant; developer-app grants are approved at creation
    is_approved boolean,
    is_revoked boolean not null default false,
    primary key (grantee_address, user_id)
);

create table if not exists core_auth_developer_apps (
    address text primary key,
    user_id bigint not null,
    is_deleted boolean not null default false
);

-- Ownership of entities whose Update/Delete requires authorization (Track,
-- Playlist).
create table if not exists core_auth_entities (
    entity_type text not null,
    entity_id bigint not null,
    owner_user_id bigint not null,
    is_deleted boolean not null default false,
    primary key (entity_type, entity_id)
);

-- +migrate Down
drop table if exists core_auth_entities;
drop table if exists core_auth_developer_apps;
drop table if exists core_auth_grants;
drop table if exists core_auth_users;
