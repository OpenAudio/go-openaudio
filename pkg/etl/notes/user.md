# User Entity Manager – Skipped Side Effects

Side effects that the discovery-provider celery indexer performs but the ETL indexer skips:

## User Update

- **ChallengeEvent.profile_update**: Challenge event for profile updates (rewards).
- **UserPayoutWalletHistory**: Insert on `spl_usdc_payout_wallet` change (skipped; payout wallet updates not indexed).

## User Verify

- **ChallengeEvent.connect_verified**: Challenge event for verification (rewards).
- **VerifiedAddress**: When empty, ETL accepts any signer (TODO: make configurable from shared_config).
