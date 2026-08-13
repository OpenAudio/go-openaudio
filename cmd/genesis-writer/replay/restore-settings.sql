-- Run AFTER a replay completes, before serving traffic.
ALTER SYSTEM RESET checkpoint_timeout;
ALTER SYSTEM RESET max_wal_size;
SELECT pg_reload_conf();
\echo 'Now run recreate-serving-indexes.sql, then VACUUM ANALYZE.'
