package server

import (
	"context"
	"time"

	"github.com/oklog/ulid/v2"
	"go.uber.org/zap"
)

const (
	opsPruneBatchSize  = 20000
	opsPruneBatchPause = 200 * time.Millisecond
)

// The crudr op-log ("ops" table) is append-only and dominates DB size on long-lived
// nodes, where almost all rows are superseded update snapshots. Pruning old rows only
// affects a peer sweeping with a cursor older than the retention window (offline that
// long, or bootstrapping from genesis); caught-up peers sweep every ~10min and never
// read that far back. It never touches the underlying replicated tables, so this
// node's serving and indexing are unaffected.
func (ss *MediorumServer) startOpsPruner(ctx context.Context) error {
	logger := ss.logger.With(zap.String("task", "ops_prune"))
	if !ss.Config.OpsPruneEnabled {
		logger.Info("ops pruning disabled")
		<-ctx.Done()
		return ctx.Err()
	}

	// brief startup delay before the first run, then prune on the configured interval
	ticker := time.NewTicker(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			ticker.Reset(ss.Config.OpsPruneInterval)
			deleted, err := ss.pruneOps(ctx, ss.Config.OpsRetention)
			if err != nil && ctx.Err() == nil {
				logger.Error("ops prune failed", zap.Int64("deleted", deleted), zap.Error(err))
			} else if deleted > 0 {
				logger.Info("ops prune complete", zap.Int64("deleted", deleted))
			}
		}
	}
}

// pruneOps deletes ops older than retention in indexed batches that each commit
// independently, avoiding a long lock or oversized transaction. The cutoff is the
// smallest ULID at (now - retention), so ulid < cutoff selects exactly the older rows.
func (ss *MediorumServer) pruneOps(ctx context.Context, retention time.Duration) (int64, error) {
	var cutoff ulid.ULID
	if err := cutoff.SetTime(ulid.Timestamp(time.Now().Add(-retention))); err != nil {
		return 0, err
	}
	cutoffStr := cutoff.String()

	var total int64
	for {
		res := ss.crud.DB.WithContext(ctx).Exec(
			`DELETE FROM ops WHERE ulid IN (SELECT ulid FROM ops WHERE ulid < ? ORDER BY ulid LIMIT ?)`,
			cutoffStr, opsPruneBatchSize)
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < opsPruneBatchSize {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(opsPruneBatchPause):
		}
	}
}
