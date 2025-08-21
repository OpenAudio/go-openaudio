-- name: InsertRegisteredEndpoint :exec
insert into eth_registered_endpoints (id, service_type, owner, delegate_wallet, endpoint, blocknumber, registered_at)
values ($1, $2, $3, $4, $5, $6, $7);

-- name: ClearRegisteredEndpoints :exec
delete from eth_registered_endpoints;

-- name: DeleteRegisteredEndpoint :exec
delete from eth_registered_endpoints
where id = $1 and endpoint = $2 and owner = $3 and service_type = $4;

-- name: ClearServiceProviders :exec
delete from eth_service_providers;

-- name: InsertServiceProvider :exec
insert into eth_service_providers (address, deployer_stake, deployer_cut, valid_bounds, number_of_endpoints, min_account_stake, max_account_stake)
values ($1, $2, $3, $4, $5, $6, $7);

-- name: UpsertServiceProvider :exec
insert into eth_service_providers (address, deployer_stake, deployer_cut, valid_bounds, number_of_endpoints, min_account_stake, max_account_stake)
values ($1, $2, $3, $4, $5, $6, $7)
on conflict (address) do update
set 
    deployer_stake = $2,
    deployer_cut = $3,
    valid_bounds = $4,
    number_of_endpoints = $5,
    min_account_stake = $6,
    max_account_stake = $7;

-- name: ClearStaked :exec
delete from eth_staked;

-- name: UpsertStaked :exec
insert into eth_staked (address, total_staked)
values ($1, $2)
on conflict (address) do update
set total_staked = $2;
