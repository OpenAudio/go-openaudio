ALTER TABLE comments
  DROP COLUMN IF EXISTS video_url,
  DROP COLUMN IF EXISTS is_members_only;
