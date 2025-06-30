create table if not exists eth_registered_endpoints(
  id int,
  service_type text,
  owner text not null,
  delegate_wallet text not null,
  endpoint text not null,
  blocknumber bigint not null,
  primary key (id, service_type)
);

create index if not exists eth_registered_endpoints_wallet_idx on eth_registered_endpoints(delegate_wallet);

create table if not exists eth_funding_rounds(
  round_num int primary key,
  blocknumber bigint not null,
  creation_time timestamp not null
);

create table if not exists eth_service_providers(
  address text primary key,
  deployer_stake bigint not null,
  deployer_cut bigint not null,
  valid_bounds boolean not null,
  number_of_endpoints int not null,
  min_account_stake bigint not null,
  max_account_stake bigint not null
);
