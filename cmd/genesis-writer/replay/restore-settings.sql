-- Run AFTER a replay completes, before serving traffic.
ALTER TABLE etl_transactions    SET (autovacuum_enabled = true);
ALTER TABLE etl_manage_entities SET (autovacuum_enabled = true);
ALTER TABLE etl_addresses       SET (autovacuum_enabled = true);
ALTER TABLE etl_plays           SET (autovacuum_enabled = true);

ALTER SYSTEM RESET checkpoint_timeout;
ALTER SYSTEM RESET max_wal_size;
SELECT pg_reload_conf();

SET maintenance_work_mem = '4GB';
SET max_parallel_maintenance_workers = 4;
\echo 'Now run the captured pg_get_indexdef statements, then VACUUM ANALYZE.'
