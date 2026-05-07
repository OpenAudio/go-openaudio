DROP TRIGGER IF EXISTS on_share ON shares;
DROP TRIGGER IF EXISTS on_event ON events;
DROP TRIGGER IF EXISTS on_comment ON comments;

DROP FUNCTION IF EXISTS handle_share();
DROP FUNCTION IF EXISTS handle_event();
DROP FUNCTION IF EXISTS handle_comment();

ALTER TABLE aggregate_user DROP COLUMN IF EXISTS track_share_count;
ALTER TABLE aggregate_playlist DROP COLUMN IF EXISTS share_count;
ALTER TABLE aggregate_track DROP COLUMN IF EXISTS share_count;
ALTER TABLE aggregate_track DROP COLUMN IF EXISTS comment_count;
