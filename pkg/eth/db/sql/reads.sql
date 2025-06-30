-- name: GetRegisteredEndpoints :many
select * from eth_registered_endpoints;

-- name: GetRegisteredEndpoint :one
select * from eth_registered_endpoints
where endpoint = $1;

-- name: GetServiceProviders :many
select * from eth_service_providers;

-- name: GetCountOfEndpointsWithDelegateWallet :one
select count(*) from eth_registered_endpoints
where delegate_wallet = $1;

-- name: GetLatestFundingRound :one
select * from eth_funding_rounds order by round_num desc limit 1;
