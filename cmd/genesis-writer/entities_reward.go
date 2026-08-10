package main

import (
	"context"
	"fmt"
	"path/filepath"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	dbm "github.com/cometbft/cometbft-db"
	cmtstore "github.com/cometbft/cometbft/store"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// writeRewards scans the old chain's blockstore for RewardPoolMessage and
// RewardMessage transactions and re-emits them verbatim into the new chain.
// The ABCI finalization layer processes them normally — signature recovery,
// pool authorization, and deadline checks all pass because the original
// signed bytes are preserved and the new chain's block heights are low
// enough that no deadline has expired.
//
// Pool transactions are emitted before reward transactions (matching the
// original chain order) so that pool authorization checks succeed.
func (w *Writer) writeRewards(ctx context.Context) error {
	// Reading the old chain's postgres is preferred: core_transactions holds
	// the same signed bytes, and a running node serves them safely, where
	// copying its live Pebble blockstore yields a torn database.
	if w.cfg.CoreDSN != "" {
		return w.writeRewardsFromDSN(ctx)
	}
	// Reaching here with no source is a mistake, not a choice: --skip-rewards
	// already exists to say "no rewards on purpose", and the step table skips
	// this function entirely when it is set. Silently writing a chain with no
	// reward history looked like a clean run once already, and the omission is
	// only discoverable by counting rows in a table nobody thought to count.
	if w.cfg.CoreCMTHome == "" {
		return fmt.Errorf("rewards need a source: pass --core-dsn (preferred) " +
			"or --core-cmt-home, or --skip-rewards to migrate without them")
	}

	dataDir := filepath.Join(w.cfg.CoreCMTHome, "data")
	bsDB, err := dbm.NewDB("blockstore", dbm.PebbleDBBackend, dataDir)
	if err != nil {
		return fmt.Errorf("open old blockstore: %w", err)
	}
	defer bsDB.Close()

	oldStore := cmtstore.NewBlockStore(bsDB)
	base := oldStore.Base()
	height := oldStore.Height()
	w.logger.Info("scanning old blockstore for reward txs",
		zap.Int64("base", base), zap.Int64("height", height))

	// Two passes: pools first, then rewards, to satisfy FK ordering.
	// Collect raw tx bytes per category.
	var poolTxBytes [][]byte
	var rewardTxBytes [][]byte

	for h := base; h <= height; h++ {
		block, _ := oldStore.LoadBlock(h)
		if block == nil {
			continue
		}
		for _, tx := range block.Txs {
			var stx corev1.SignedTransaction
			if err := proto.Unmarshal(tx, &stx); err != nil {
				continue // not a valid proto — skip
			}
			switch stx.Transaction.(type) {
			case *corev1.SignedTransaction_RewardPool:
				poolTxBytes = append(poolTxBytes, tx)
			case *corev1.SignedTransaction_Reward:
				rewardTxBytes = append(rewardTxBytes, tx)
			}
		}

		if h%100000 == 0 && h > base {
			w.logger.Info("scan progress",
				zap.Int64("height", h),
				zap.Int("pools", len(poolTxBytes)),
				zap.Int("rewards", len(rewardTxBytes)))
		}
	}

	w.logger.Info("found reward txs",
		zap.Int("pools", len(poolTxBytes)),
		zap.Int("rewards", len(rewardTxBytes)))

	// Emit pool txs first.
	for i, txBytes := range poolTxBytes {
		if err := w.addTx(ctx, txBytes); err != nil {
			return fmt.Errorf("emit reward pool tx %d: %w", i, err)
		}
	}

	// Then reward txs.
	for i, txBytes := range rewardTxBytes {
		if err := w.addTx(ctx, txBytes); err != nil {
			return fmt.Errorf("emit reward tx %d: %w", i, err)
		}
	}

	return nil
}
