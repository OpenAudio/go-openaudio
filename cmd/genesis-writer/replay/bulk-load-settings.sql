-- Run BEFORE replaying a genesis chain. Reverse with restore-settings.sql.

-- Checkpoints: a replay is one long write burst. Default 5-minute checkpoints
-- spread the same pages to disk over and over. On a 2026-08 run 1004 of 1017
-- checkpoints were timed rather than WAL-pressure requested, so the interval
-- was the binding constraint, not max_wal_size.
ALTER SYSTEM SET checkpoint_timeout = '30min';
ALTER SYSTEM SET max_wal_size = '32GB';

-- Index pages are the hot working set. If they do not fit, inserts become
-- random reads -- which is fatal when the data directory is on external
-- storage. Size this to comfortably hold the indexes that survive the drop
-- script; measure with pg_statio_user_tables and aim for a >95% hit rate.
-- REQUIRES A RESTART, so set it before the run, not during one.
ALTER SYSTEM SET shared_buffers = '12GB';

SELECT pg_reload_conf();
\echo 'checkpoint_timeout and max_wal_size are live; shared_buffers needs a restart.'
