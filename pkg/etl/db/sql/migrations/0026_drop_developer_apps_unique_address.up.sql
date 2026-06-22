-- developer_apps was created with both PRIMARY KEY (address, txhash) and
-- UNIQUE (address). The second constraint contradicts the row-versioning
-- pattern: developer_app_update and developer_app_delete both INSERT a new
-- row with the same address (different txhash) after marking the previous
-- row is_current = false. UNIQUE (address) blocks that INSERT.
--
-- Fresh databases skip the constraint via the updated 0002 migration.
-- This migration drops it for databases that ran the old 0002.

ALTER TABLE developer_apps
  DROP CONSTRAINT IF EXISTS unique_developer_apps_address;
