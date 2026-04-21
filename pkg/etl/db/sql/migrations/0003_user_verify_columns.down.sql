-- Remove user verification columns.

ALTER TABLE users DROP COLUMN IF EXISTS twitter_handle;
ALTER TABLE users DROP COLUMN IF EXISTS instagram_handle;
ALTER TABLE users DROP COLUMN IF EXISTS tiktok_handle;
ALTER TABLE users DROP COLUMN IF EXISTS verified_with_twitter;
ALTER TABLE users DROP COLUMN IF EXISTS verified_with_instagram;
ALTER TABLE users DROP COLUMN IF EXISTS verified_with_tiktok;
