-- name: GetRegisteredEndpoints :many
select * from eth_registered_endpoints;

-- name: GetRegisteredEndpoint :one
select * from eth_registered_endpoints
where endpoint = $1;

-- name: GetRegisteredEndpointsForServiceProvider :many
select * from eth_registered_endpoints
where owner = $1;


-- name: GetServiceProvider :one
select * from eth_service_providers
where address = $1;

-- name: GetServiceProviders :many
select * from eth_service_providers;

-- name: GetCountOfEndpointsWithDelegateWallet :one
select count(*) from eth_registered_endpoints
where delegate_wallet = $1;

-- name: GetLatestFundingRound :one
select * from eth_funding_rounds order by round_num desc limit 1;

-- name: GetStakedAmountForServiceProvider :one
select total_staked from eth_staked where address = $1;

-- name: GetActiveProposals :many
select * from eth_active_proposals;
