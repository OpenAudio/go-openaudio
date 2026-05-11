-- +migrate Up notransaction
DROP INDEX CONCURRENTLY IF EXISTS idx_core_transactions_tx_hash;

-- +migrate Down notransaction
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_core_transactions_tx_hash
  ON core_transactions(tx_hash);
