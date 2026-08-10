package main

import (
	"context"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// Wire tags for the two reward variants of SignedTransaction's `transaction`
// oneof. A protobuf field's tag is (field_number << 3) | wire_type, varint
// encoded; at field numbers 1009 and 1011 with wire type 2 that is two bytes:
//
//	reward       = 1009 -> 0x8a 0x3f
//	reward_pool  = 1011 -> 0x9a 0x3f
//
// They are not at a fixed offset — `signature` and `request_id` are serialized
// first and vary in length — so the scan searches for them anywhere in the row.
//
// Both tags are invalid UTF-8, and proto3 requires `string` fields to be valid
// UTF-8, so neither can occur inside `signature` or `request_id`: the obvious
// source of false positives is ruled out by the encoding itself. A collision is
// still conceivable inside a nested `bytes` field of another transaction type,
// so every match is confirmed by type before it is emitted — the byte search is
// a prefilter, not a decision. There are no false negatives: a reward
// transaction always carries its tag.
//
// Derived from the generated types rather than hand-computed; see
// TestRewardWireTagsMatchProto.
var (
	rewardTag     = []byte{0x8a, 0x3f}
	rewardPoolTag = []byte{0x9a, 0x3f}
)

const defaultCoreScanChunk int64 = 500_000

// connectOldCore opens a single deliberately-constrained connection to the old
// chain's database.
//
// This runs against a live production validator with no read replica, so the
// connection is set up to be incapable of the damage a scan could otherwise
// do: it cannot write, it cannot sit idle inside a transaction, and no single
// statement can run away. application_name is set so the query is
// recognisable in pg_stat_activity if someone needs to kill it.
func connectOldCore(ctx context.Context, dsn string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse core dsn: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = "genesis-writer-reward-scan"
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	// A chunk that cannot finish in two minutes means something is wrong;
	// failing is better than holding a snapshot open indefinitely.
	cfg.RuntimeParams["statement_timeout"] = "120000"
	cfg.RuntimeParams["idle_in_transaction_session_timeout"] = "30000"
	// Never let the scan wait behind another session's lock.
	cfg.RuntimeParams["lock_timeout"] = "5000"

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to old core db: %w", err)
	}
	return conn, nil
}

// writeRewardsFromDSN replays reward transactions read from the old chain's
// core_transactions table.
//
// The filter runs in postgres. Of ~66M transactions on the production chain,
// a few hundred are rewards, so matching server-side is the difference between
// transferring every row and transferring the ones that matter.
func (w *Writer) writeRewardsFromDSN(ctx context.Context) error {
	conn, err := connectOldCore(ctx, w.cfg.CoreDSN)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	var minBlock, maxBlock int64
	if err := conn.QueryRow(ctx,
		`SELECT COALESCE(MIN(block_id), 0), COALESCE(MAX(block_id), 0) FROM core_transactions`).
		Scan(&minBlock, &maxBlock); err != nil {
		return fmt.Errorf("read core_transactions block range: %w", err)
	}
	if maxBlock == 0 {
		w.logger.Warn("old core db has no transactions; nothing to replay")
		return nil
	}

	chunk := w.cfg.CoreScanChunk
	if chunk <= 0 {
		chunk = defaultCoreScanChunk
	}

	w.logger.Info("scanning old core db for reward txs",
		zap.Int64("min_block", minBlock),
		zap.Int64("max_block", maxBlock),
		zap.Int64("chunk", chunk),
		zap.Bool("dry_run", w.cfg.CoreScanDryRun))

	var poolTxBytes, rewardTxBytes [][]byte
	var falsePositives int
	start := time.Now()

	// One statement per window, each its own implicit transaction. A single
	// scan over the whole table would pin the xmin horizon for its duration
	// and block autovacuum cleanup across the database.
	for lo := minBlock; lo <= maxBlock; lo += chunk {
		hi := lo + chunk
		rows, err := conn.Query(ctx, `
			SELECT transaction
			FROM core_transactions
			WHERE block_id >= $1 AND block_id < $2
			  AND (position($3::bytea in transaction) > 0
			    OR position($4::bytea in transaction) > 0)
			ORDER BY block_id, index
		`, lo, hi, rewardTag, rewardPoolTag)
		if err != nil {
			return fmt.Errorf("scan core_transactions blocks [%d,%d): %w", lo, hi, err)
		}

		var window [][]byte
		for rows.Next() {
			var txBytes []byte
			if err := rows.Scan(&txBytes); err != nil {
				rows.Close()
				return fmt.Errorf("scan reward tx row: %w", err)
			}
			window = append(window, txBytes)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read reward rows [%d,%d): %w", lo, hi, err)
		}

		// Confirm by type. The byte search is a prefilter, not a decision.
		for _, txBytes := range window {
			var stx corev1.SignedTransaction
			if err := proto.Unmarshal(txBytes, &stx); err != nil {
				falsePositives++
				continue
			}
			switch stx.Transaction.(type) {
			case *corev1.SignedTransaction_RewardPool:
				poolTxBytes = append(poolTxBytes, txBytes)
			case *corev1.SignedTransaction_Reward:
				rewardTxBytes = append(rewardTxBytes, txBytes)
			default:
				falsePositives++
			}
		}

		if lo/chunk%20 == 0 {
			w.logger.Info("scan progress",
				zap.Int64("block", hi),
				zap.Int64("max_block", maxBlock),
				zap.Int("pools", len(poolTxBytes)),
				zap.Int("rewards", len(rewardTxBytes)))
		}
	}

	w.logger.Info("found reward txs",
		zap.Int("pools", len(poolTxBytes)),
		zap.Int("rewards", len(rewardTxBytes)),
		zap.Int("prefilter_false_positives", falsePositives),
		zap.Duration("scan_elapsed", time.Since(start)))

	if w.cfg.CoreScanDryRun {
		w.logger.Info("dry run: emitting nothing. Reconcile these against the old chain " +
			"(select count(*) from core_reward_pools / core_rewards) before a real run.")
		return nil
	}

	// Pools before rewards: a reward references its pool, and the pool has to
	// exist first for the foreign key to hold.
	for i, txBytes := range poolTxBytes {
		if err := w.addTx(ctx, txBytes); err != nil {
			return fmt.Errorf("emit reward pool tx %d: %w", i, err)
		}
	}
	for i, txBytes := range rewardTxBytes {
		if err := w.addTx(ctx, txBytes); err != nil {
			return fmt.Errorf("emit reward tx %d: %w", i, err)
		}
	}
	return nil
}
