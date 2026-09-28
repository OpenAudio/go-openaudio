-- +migrate Up
-- The initial backfill scans once at startup. Hold off writers until both the
-- baseline and triggers are installed, so concurrent inserts cannot be missed.
LOCK TABLE core_tx_stats IN SHARE ROW EXCLUSIVE MODE;
CREATE TABLE IF NOT EXISTS core_tx_count (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    total bigint NOT NULL CHECK (total >= 0)
);
INSERT INTO core_tx_count (singleton, total)
SELECT true, count(*) FROM core_tx_stats
ON CONFLICT (singleton) DO UPDATE SET total = EXCLUDED.total;

-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION maintain_core_tx_count() RETURNS trigger AS $$
DECLARE
    delta bigint;
BEGIN
    IF TG_OP = 'TRUNCATE' THEN
        -- Do not recreate a row while RestoreDatabase truncates all tables;
        -- the snapshot may subsequently COPY its own counter row.
        UPDATE core_tx_count SET total = 0 WHERE singleton;
        RETURN NULL;
    ELSIF TG_OP = 'INSERT' THEN
        SELECT count(*) INTO delta FROM inserted_rows;
    ELSE
        SELECT -count(*) INTO delta FROM deleted_rows;
    END IF;
    IF delta <> 0 THEN
        UPDATE core_tx_count SET total = total + delta WHERE singleton;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'core transaction count is not initialized';
        END IF;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +migrate StatementEnd

DROP TRIGGER IF EXISTS core_tx_count_insert ON core_tx_stats;
CREATE TRIGGER core_tx_count_insert AFTER INSERT ON core_tx_stats
REFERENCING NEW TABLE AS inserted_rows
FOR EACH STATEMENT EXECUTE FUNCTION maintain_core_tx_count();
DROP TRIGGER IF EXISTS core_tx_count_delete ON core_tx_stats;
CREATE TRIGGER core_tx_count_delete AFTER DELETE ON core_tx_stats
REFERENCING OLD TABLE AS deleted_rows
FOR EACH STATEMENT EXECUTE FUNCTION maintain_core_tx_count();
DROP TRIGGER IF EXISTS core_tx_count_truncate ON core_tx_stats;
CREATE TRIGGER core_tx_count_truncate AFTER TRUNCATE ON core_tx_stats
FOR EACH STATEMENT EXECUTE FUNCTION maintain_core_tx_count();

-- +migrate Down
DROP TRIGGER IF EXISTS core_tx_count_insert ON core_tx_stats;
DROP TRIGGER IF EXISTS core_tx_count_delete ON core_tx_stats;
DROP TRIGGER IF EXISTS core_tx_count_truncate ON core_tx_stats;
DROP FUNCTION IF EXISTS maintain_core_tx_count();
DROP TABLE IF EXISTS core_tx_count;
