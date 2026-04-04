# Track Entity Manager – Skipped Side Effects

Side effects that the discovery-provider celery indexer performs but the ETL indexer skips:

## Track Create / Update

- **ChallengeEvent.track_upload**: Reward challenge on upload.
- **Stems / remixes tables**: `update_stems_table`, `update_remixes_table` (skipped).
- **TrackPriceHistory**: Price / gated-condition history (skipped).
- **Remix contest notifications**: `create_remix_contest_notification_helper` (skipped).
- **DDEX validation**: `validate_update_ddex_track` (signer vs `ddex_app`) is deferred per parity plan.
