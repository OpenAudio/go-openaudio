-- +migrate Up

-- These 5 Figment validators exist in CometBFT's validator set but are missing
-- from core_validators. They were registered via the legacy registration path
-- which did not write to validator_history, and their core_validators rows were
-- lost (likely during a deregistration/re-registration cycle where the DB insert
-- was skipped due to duplicate detection but the CometBFT ValidatorUpdate was
-- still delivered).
--
-- Without these rows, the SLA rollup reports list 45 validators while CometBFT
-- has 50, causing every rollup to be rejected and halting the chain.

INSERT INTO core_validators (pub_key, endpoint, eth_address, comet_address, comet_pub_key, eth_block, node_type, sp_id)
SELECT '03ac774f287e9f7f6058b43a711b2b70ee4b2fd4d0eaf286fbaef049f83fc28fa5', 'https://audius-content-10.figment.io', '0xff753331CEa586DD5B23bd21222a3c902909F2dd', 'A5B56BBFA35E2818A915CFAAEA5A0676C8CDB68E', 'l2RfFOHYFEyGDj6dET9AzPT9Cxjw7c0QlRBVY7Ejczs=', '24599489', 'validator', '69'
WHERE NOT EXISTS (SELECT 1 FROM core_validators WHERE comet_address = 'A5B56BBFA35E2818A915CFAAEA5A0676C8CDB68E');

INSERT INTO core_validators (pub_key, endpoint, eth_address, comet_address, comet_pub_key, eth_block, node_type, sp_id)
SELECT '0305e48ef93f77b114a093545c6482e815c9dd747c25f777940e1061a322c68fa0', 'https://audius-content-11.figment.io', '0xC9721F892BcC8822eb34237E875BE93904f11073', 'B5EF07A27E9A053561C578504F2649E406804E06', '4oB3rW0TvgDywNa2FZk6fWrOoseVar/TWNtDhvpL3RM=', '24599502', 'validator', '70'
WHERE NOT EXISTS (SELECT 1 FROM core_validators WHERE comet_address = 'B5EF07A27E9A053561C578504F2649E406804E06');

INSERT INTO core_validators (pub_key, endpoint, eth_address, comet_address, comet_pub_key, eth_block, node_type, sp_id)
SELECT '0392e8771567e8c75219b8fb19b02227ee21f62d73340489bce93caaba999603df', 'https://audius-content-12.figment.io', '0x780641e157621621658F118375dc1B36Ea514d46', '56049970FBAD44D540B8BEF6118800433D269049', '/jC9b7b5Glfg0RvmjZpyJIxcPdJNLyaA1QR1L+2qCkg=', '24599551', 'validator', '73'
WHERE NOT EXISTS (SELECT 1 FROM core_validators WHERE comet_address = '56049970FBAD44D540B8BEF6118800433D269049');

INSERT INTO core_validators (pub_key, endpoint, eth_address, comet_address, comet_pub_key, eth_block, node_type, sp_id)
SELECT '032ca8aac18bc14d0c0d834c8c7a530058fe4718a65c50f2700db7ad282d45a069', 'https://audius-content-13.figment.io', '0x33a2da466B14990E0124383204b06F9196f62d8e', '86A2636C1650226B89755828542D529ADB028BC6', '9SNwOOMcZDJwNVNoVMBwou73YpWVa8it1WB5XPXCdsY=', '24599517', 'validator', '71'
WHERE NOT EXISTS (SELECT 1 FROM core_validators WHERE comet_address = '86A2636C1650226B89755828542D529ADB028BC6');

INSERT INTO core_validators (pub_key, endpoint, eth_address, comet_address, comet_pub_key, eth_block, node_type, sp_id)
SELECT '0253d06217adde4b689f86b13b0c78342440cc22bdd047e75de69e2fa4b0131f29', 'https://audius-content-14.figment.io', '0x817c513C1B702eA0BdD4F8C1204C60372f715006', 'C8C249813AC90623B86AF281D06E88CA4686D555', 'wzqUOH0cyv5b7B0ppzrjqW47jeZeUy9pxLAHM2ZRsc0=', '24599531', 'validator', '72'
WHERE NOT EXISTS (SELECT 1 FROM core_validators WHERE comet_address = 'C8C249813AC90623B86AF281D06E88CA4686D555');

-- +migrate Down
DELETE FROM core_validators WHERE comet_address IN (
    'A5B56BBFA35E2818A915CFAAEA5A0676C8CDB68E',
    'B5EF07A27E9A053561C578504F2649E406804E06',
    '56049970FBAD44D540B8BEF6118800433D269049',
    '86A2636C1650226B89755828542D529ADB028BC6',
    'C8C249813AC90623B86AF281D06E88CA4686D555'
);
