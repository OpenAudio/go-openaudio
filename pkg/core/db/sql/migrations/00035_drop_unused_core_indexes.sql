-- +migrate Up notransaction
DROP INDEX CONCURRENTLY IF EXISTS idx_core_blocks_hash;
DROP INDEX CONCURRENTLY IF EXISTS idx_core_tx_stats_time_type;
DROP INDEX CONCURRENTLY IF EXISTS idx_core_blocks_chain_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_core_blocks_proposer;

-- +migrate Down notransaction
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_core_blocks_hash
  ON core_blocks(hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_core_tx_stats_time_type
  ON core_tx_stats(created_at, tx_type);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_core_blocks_chain_id
  ON core_blocks(chain_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_core_blocks_proposer
  ON core_blocks(proposer);
