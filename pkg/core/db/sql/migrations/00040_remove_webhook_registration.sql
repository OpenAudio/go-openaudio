-- +migrate Up
-- Remove a legacy registration on audius-mainnet-alpha-beta (height 35004574)
-- whose signer is not in the eth registry and which has no comet key. The
-- validator warden cannot deregister it through attestations, so it is removed
-- directly. Matching on every identifying column makes this a no-op elsewhere.
DELETE FROM core_validators
WHERE eth_address = '0x3B7cEAB73AEce90ab4E600aCABC8B407c7257E59'
  AND endpoint = 'https://webhook.site/5838668c-998e-411e-92a7-d52924b6c16c'
  AND comet_address = ''
  AND comet_pub_key = '';

-- +migrate Down
-- The removed row is not restored.
select 1;
