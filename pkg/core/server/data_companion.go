package server

import (
	"context"
	"errors"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/cometbft/cometbft/rpc/grpc/client/privileged"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// calculateLowestRetainHeight returns the next retain height (or 0 for "no change").
//
// Behavior:
//   - Archive mode => never prune.
//   - If serving snapshots and we have any, set retain height to
//     (oldestSnapshotHeight - safetyBuffer) and stop.
//   - Otherwise, use the configured retain window: latestHeight - retainWindow.
//   - Retain height only moves forward (monotonic); return 0 if it wouldn't advance.
func (s *Server) calculateLowestRetainHeight(ctx context.Context) int64 {
	if s.config.Archive {
		return 0 // Archive nodes keep every block forever
	}

	latestBlock, err := s.db.GetLatestBlock(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0 // chain is empty, nothing to prune
		}
		s.logger.Error("could not get latest block, can't prune", zap.Error(err))
		return 0
	}
	latestHeight := latestBlock.Height
	lastSetRetain := s.abciState.lastRetainHeight

	// 1) STATE SYNC SNAPSHOT-BASED RETAIN LOGIC
	//
	// If we're serving snapshots, we must keep the blocks needed to validate
	// the *oldest* snapshot we currently have. This is because nodes state-syncing
	// from us may still need those historical blocks to verify snapshot chunks.
	//
	// Example:
	//   Snapshots sorted newest→oldest: [950, 900, 850]
	//   Oldest snapshot = 850
	//   safetyBuffer = 100
	//   => retain height = 750
	//
	// This means: prune everything below 750, but keep 750..latest.
	// That way, snapshot at 850 is still fully verifiable.
	if s.config.StateSync.ServeSnapshots {
		if si, ok := s.cache.snapshotInfo.Get(SnapshotInfoKey); ok && len(si.Snapshots) > 0 {
			const safetyBuffer int64 = 100

			// Snapshots are stored DESC (newest→oldest). Use the oldest one.
			oldestSnapshotHeight := si.Snapshots[len(si.Snapshots)-1].Height

			// Keep some extra blocks before the snapshot start just in case.
			retainFromSnapshots := oldestSnapshotHeight - safetyBuffer
			if retainFromSnapshots < 1 {
				retainFromSnapshots = 1
			}

			// Monotonic: only advance retain height.
			if retainFromSnapshots > lastSetRetain {
				return retainFromSnapshots
			}
			return 0
		}
	}

	// 2) RETAIN-WINDOW-BASED PRUNING
	//
	// If no snapshots are served (or none in cache), fall back to the configured
	// retain window. This just keeps a rolling window of recent blocks.
	//
	// Example:
	//   latestHeight = 1000
	//   retainWindow = 200
	//   => retain height = 800
	//
	// This means: prune everything below 800, keep 800..latest.
	retainWindow := s.config.RetainHeight
	if retainWindow <= 0 || latestHeight <= retainWindow {
		return 0 // invalid config or chain too short to prune
	}

	retainFromWindow := latestHeight - retainWindow
	if retainFromWindow <= lastSetRetain {
		return 0 // would not advance retain height
	}
	return retainFromWindow
}

func (s *Server) startDataCompanion(ctx context.Context) error {
	s.StartProcess(ProcessStateDataCompanion)

	if s.config.Archive {
		s.CompleteProcess(ProcessStateDataCompanion)
		return nil
	}

	s.logger.Info("starting data companion")

	select {
	case <-ctx.Done():
		s.CompleteProcess(ProcessStateDataCompanion)
		return ctx.Err()
	case <-s.awaitRpcReady:
	}

	s.RunningProcess(ProcessStateDataCompanion)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.manageBlockRetention(ctx)
		case <-ctx.Done():
			s.CompleteProcess(ProcessStateDataCompanion)
			return ctx.Err()
		}
	}
}

func (s *Server) manageBlockRetention(ctx context.Context) {
	s.RunningProcessWithMetadata(ProcessStateDataCompanion, "Managing block retention")

	// Create fresh connection each cycle to diagnose issues
	conn, err := privileged.New(ctx, config.PrivilegedServiceSocketURI, privileged.WithPruningServiceEnabled(true), privileged.WithInsecure())
	if err != nil {
		s.logger.Error("dc could not connect to privileged socket",
			zap.String("socket", config.PrivilegedServiceSocketURI),
			zap.Error(err))
		s.SleepingProcessWithMetadata(ProcessStateDataCompanion, "Waiting after connection error")
		return
	}
	defer conn.Close()

	blockRetainHeight, err := conn.GetBlockRetainHeight(ctx)
	if err != nil {
		s.logger.Error("dc could not get block retain height",
			zap.String("socket", config.PrivilegedServiceSocketURI),
			zap.Error(err))
		s.SleepingProcessWithMetadata(ProcessStateDataCompanion, "Waiting after error")
		return
	}

	if blockRetainHeight.App <= 1 {
		s.SleepingProcessWithMetadata(ProcessStateDataCompanion, "Waiting for blocks to accumulate")
		return
	}

	if err := conn.SetBlockRetainHeight(ctx, blockRetainHeight.App); err != nil {
		s.logger.Error("dc could not set block retain height", zap.Error(err))
	}

	if err := conn.SetBlockResultsRetainHeight(ctx, blockRetainHeight.App); err != nil {
		s.logger.Error("dc could not set block results retain height", zap.Error(err))
	}

	s.SleepingProcessWithMetadata(ProcessStateDataCompanion, "Waiting for next cycle")
}
