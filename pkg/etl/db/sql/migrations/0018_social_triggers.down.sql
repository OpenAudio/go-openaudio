DROP TRIGGER IF EXISTS on_repost ON reposts;
DROP TRIGGER IF EXISTS on_save ON saves;
DROP TRIGGER IF EXISTS on_follow ON follows;

DROP FUNCTION IF EXISTS handle_repost();
DROP FUNCTION IF EXISTS handle_save();
DROP FUNCTION IF EXISTS handle_follow();

ALTER TABLE aggregate_user DROP COLUMN IF EXISTS score;
