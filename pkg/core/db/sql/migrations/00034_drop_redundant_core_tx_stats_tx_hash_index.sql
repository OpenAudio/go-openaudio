-- +migrate Up notransaction
DROP INDEX CONCURRENTLY IF EXISTS idx_core_tx_hash;

-- +migrate Down notransaction
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_core_tx_hash
  ON core_tx_stats(tx_hash);
