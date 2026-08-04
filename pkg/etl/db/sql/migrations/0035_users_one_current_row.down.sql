-- The deleted rows are not restored: they were duplicates that should never
-- have been current, and nothing records which ones this migration removed.
DROP INDEX IF EXISTS users_current_uniq_idx;
