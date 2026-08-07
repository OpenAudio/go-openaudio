-- Run AFTER the ETL migrations, BEFORE replaying. Nothing is deleted during a
-- replay, so autovacuum only competes for IO. Re-enable and ANALYZE after.
ALTER TABLE etl_transactions    SET (autovacuum_enabled = false);
ALTER TABLE etl_manage_entities SET (autovacuum_enabled = false);
ALTER TABLE etl_addresses       SET (autovacuum_enabled = false);
ALTER TABLE etl_plays           SET (autovacuum_enabled = false);
