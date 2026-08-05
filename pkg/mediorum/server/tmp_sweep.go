package server

import (
	"context"
	"errors"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/persistence"
	"go.uber.org/zap"
)

// startStaleTempSweeper removes orphaned fileblob ".tmp" files from every
// file:// bucket, once, in the background.
//
// This used to run synchronously inside persistence.Open, which put a full
// recursive walk of each bucket on the startup critical path: mediorum could
// not initialize until it finished, so the echo server never bound :1991 and
// the PoS handler never read its channel. On a store-all archive holding
// ~1M objects across ~1.4M single-file directories that walk takes hours,
// during which the node serves no content and answers no storage proofs.
//
// Nothing depends on the sweep having completed. A ".tmp" file is inert: no key
// resolves to it, readers never open it, and both repair's presence index and
// fileblob's own List skip the suffix. The only cost of leaving one in place is
// the disk space it occupies, so the work belongs off the critical path.
func (ss *MediorumServer) startStaleTempSweeper(ctx context.Context) error {
	dsns := []string{ss.Config.BlobStoreDSN}
	if ss.Config.ArchiveBlobStoreDSN != "" {
		dsns = append(dsns, ss.Config.ArchiveBlobStoreDSN)
	}

	for _, dsn := range dsns {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dir, isFile := persistence.FileDirFromDSN(dsn)
		if !isFile {
			// Cloud backends have no local tree and leave no .tmp files.
			continue
		}

		logger := ss.logger.With(zap.String("task", "tmpSweep"), zap.String("dir", dir))
		logger.Info("sweeping stale temp files")

		start := time.Now()
		removed, err := persistence.SweepStaleTempFiles(ctx, dsn, persistence.DefaultStaleTempFileAge)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			logger.Warn("stale temp file sweep failed",
				zap.Int("removed", removed), zap.Duration("took", time.Since(start)), zap.Error(err))
			continue
		}

		logger.Info("stale temp file sweep complete",
			zap.Int("removed", removed), zap.Duration("took", time.Since(start)))
	}

	return nil
}
