-- Optimize location-based queries with composite indexes
-- +migrate Up

-- Drop existing single-column indexes as they're now covered by the composite indexes
DROP INDEX IF EXISTS core_etl_tx_plays_city_idx;
DROP INDEX IF EXISTS core_etl_tx_plays_region_idx;
DROP INDEX IF EXISTS core_etl_tx_plays_country_idx;

-- Create optimized composite indexes for location queries
CREATE INDEX core_etl_tx_plays_city_region_country_plays_idx ON core_etl_tx_plays (country, region, city) 
WHERE city IS NOT NULL;

CREATE INDEX core_etl_tx_plays_region_country_plays_idx ON core_etl_tx_plays (country, region) 
WHERE region IS NOT NULL;

CREATE INDEX core_etl_tx_plays_country_plays_idx ON core_etl_tx_plays (country);

-- +migrate Down

-- Drop the optimized indexes
DROP INDEX IF EXISTS core_etl_tx_plays_city_region_country_plays_idx;
DROP INDEX IF EXISTS core_etl_tx_plays_region_country_plays_idx;
DROP INDEX IF EXISTS core_etl_tx_plays_country_plays_idx;
