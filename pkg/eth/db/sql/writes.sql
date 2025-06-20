-- name: InsertRegisteredEndpoint :exec
insert into eth_registered_endpoints (id, service_type, owner, delegate_wallet, endpoint, blocknumber)
values ($1, $2, $3, $4, $5, $6);

-- name: ClearRegisteredEndpoints :exec
delete from eth_registered_endpoints;

-- name: DeleteRegisteredEndpoint :exec
delete from eth_registered_endpoints
where id = $1 and endpoint = $2 and owner = $3 and service_type = $4;
