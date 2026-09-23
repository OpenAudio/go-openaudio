package db

import "context"

// EnsureTransactionCount seeds the counter after restoring a snapshot produced
// before the counter existed. New snapshots carry the counter in the same
// pg_dump snapshot as core_tx_stats and need no full-table scan.
func (q *Queries) EnsureTransactionCount(ctx context.Context) error {
	// A single statement's snapshot makes the fallback atomic. Snapshot restore
	// runs before block processing resumes, so there are no concurrent writers.
	_, err := q.db.Exec(ctx, `
  INSERT INTO core_tx_count (singleton, total)
  SELECT true, count(*) FROM core_tx_stats
  WHERE NOT EXISTS (SELECT 1 FROM core_tx_count WHERE singleton)
  HAVING NOT EXISTS (SELECT 1 FROM core_tx_count WHERE singleton)
  ON CONFLICT (singleton) DO NOTHING`)
	return err
}
