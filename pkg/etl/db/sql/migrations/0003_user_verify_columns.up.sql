-- Add user verification/social columns (matching discovery-provider 0073_add_user_socials).
-- Required for User Verify handler.

ALTER TABLE users ADD COLUMN IF NOT EXISTS twitter_handle character varying;
ALTER TABLE users ADD COLUMN IF NOT EXISTS instagram_handle character varying;
ALTER TABLE users ADD COLUMN IF NOT EXISTS tiktok_handle character varying;
ALTER TABLE users ADD COLUMN IF NOT EXISTS verified_with_twitter boolean DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS verified_with_instagram boolean DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS verified_with_tiktok boolean DEFAULT false;
