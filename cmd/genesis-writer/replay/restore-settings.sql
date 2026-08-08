-- Run AFTER a replay completes, before serving traffic.
ALTER TABLE etl_transactions    SET (autovacuum_enabled = true);
ALTER TABLE etl_manage_entities SET (autovacuum_enabled = true);
ALTER TABLE etl_addresses       SET (autovacuum_enabled = true);
ALTER TABLE etl_plays           SET (autovacuum_enabled = true);

ALTER SYSTEM RESET checkpoint_timeout;
ALTER SYSTEM RESET max_wal_size;
SELECT pg_reload_conf();
\echo 'Now run recreate-serving-indexes.sql, then VACUUM ANALYZE.'
