create table if not exists eth_active_proposals (
    id bigint primary key,
    proposer text not null,
    submission_block_number bigint not null,
    target_contract_registry_key text not null,
    target_contract_address text not null,
    call_value bigint not null,
    function_signature text not null,
    call_data text not null
);
