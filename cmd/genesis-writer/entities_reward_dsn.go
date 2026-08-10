package main

import (
	"context"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/google/uuid"
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

	pools, err := readRewardPools(ctx, conn)
	if err != nil {
		return err
	}

	w.logger.Info("found reward txs",
		zap.Int("scanned_pool_txs", len(poolTxBytes)),
		zap.Int("rewards", len(rewardTxBytes)),
		zap.Int("pools_in_state", len(pools)),
		zap.Int("prefilter_false_positives", falsePositives),
		zap.Duration("scan_elapsed", time.Since(start)))

	// The scanned pool transactions are read for reconciliation and then
	// dropped; the pools are emitted from state instead. See
	// synthesizeRewardPoolTxs.
	for _, p := range pools {
		w.logger.Info("reward pool",
			zap.String("rewards_manager_pubkey", p.RewardsManagerPubkey),
			zap.Strings("authorities", p.Authorities))
	}

	if w.cfg.CoreScanDryRun {
		w.logger.Info("dry run: emitting nothing. Reconcile these against the old chain " +
			"(select count(*) from core_reward_pools / core_rewards) before a real run.")
		return nil
	}

	poolCreateTxs, err := w.synthesizeRewardPoolTxs(pools)
	if err != nil {
		return err
	}

	// Pools before rewards: a reward references its pool, and the pool has to
	// exist first for the foreign key to hold.
	for i, txBytes := range poolCreateTxs {
		if err := w.addTx(ctx, txBytes); err != nil {
			return fmt.Errorf("emit reward pool create tx %d: %w", i, err)
		}
	}
	for i, txBytes := range rewardTxBytes {
		if err := w.addTx(ctx, txBytes); err != nil {
			return fmt.Errorf("emit reward tx %d: %w", i, err)
		}
	}

	w.logger.Info("emitted reward txs",
		zap.Int("pool_creates", len(poolCreateTxs)),
		zap.Int("dropped_pool_txs", len(poolTxBytes)),
		zap.Int("rewards", len(rewardTxBytes)))
	return nil
}

// rewardPool is one row of the old chain's materialized pool state.
type rewardPool struct {
	RewardsManagerPubkey string
	Authorities          []string
}

// readRewardPools reads the old chain's core_reward_pools.
//
// This is the authority on what pools exist, and the pool transactions are
// not: of the four pools on the production chain, only ONE was ever created by
// a transaction. The other three were materialized by
// 00034_reward_pools.sql, which DERIVES pools from core_rewards as a per-
// manager union of claim authorities. That derivation is a hotfix artifact —
// pools were introduced to contain a leaked deterministic secret by giving the
// authority somewhere to be rotated out of — and it leaves the pool set
// reconstructible only by re-running a migration against a table it also
// rewrites. Reading the rows and re-emitting them as transactions is what
// retires it.
func readRewardPools(ctx context.Context, conn *pgx.Conn) ([]rewardPool, error) {
	rows, err := conn.Query(ctx,
		`SELECT rewards_manager_pubkey, authorities FROM core_reward_pools ORDER BY rewards_manager_pubkey`)
	if err != nil {
		return nil, fmt.Errorf("read core_reward_pools: %w", err)
	}
	defer rows.Close()

	var pools []rewardPool
	for rows.Next() {
		var p rewardPool
		if err := rows.Scan(&p.RewardsManagerPubkey, &p.Authorities); err != nil {
			return nil, fmt.Errorf("scan reward pool row: %w", err)
		}
		pools = append(pools, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read reward pool rows: %w", err)
	}
	return pools, nil
}

// synthesizeRewardPoolTxs turns each old-chain pool row into an explicit
// CreateRewardPool transaction carrying that pool's FINAL authority set.
//
// Creating at the end state is what makes the old chain's two
// SetRewardPoolAuthorities transactions redundant, and they are not replayed.
// Both were rotations off a launchpad key derived from the leaked
// deterministic secret; replaying create-then-rotate would put the compromised
// key back on the new chain for the intervening blocks, and replaying the
// rotations without their creates is not possible, because two of the three
// pools they targeted never had a create.
//
// # Signatures
//
// The envelope signature and rm_owner_signature are left EMPTY, and that is
// deliberate rather than a gap to be filled later.
//
// A CreateRewardPool that satisfies validateCreateRewardPool needs two
// signatures we do not have and must not manufacture. rm_owner_signature is
// ed25519 over the body by the reward manager keypair, which only the
// launchpad's deterministic secret can produce — being unable to produce it is
// the security property, not an obstacle. The envelope's secp256k1 signer must
// additionally be a member of msg.authorities, and for the two rotated pools
// those are the post-rotation keys, held by the launchpad. So there is no
// partially-signed form that validates: signing the envelope with the
// migration key would produce a transaction that still fails, while looking
// like it was meant to pass.
//
// Nothing re-validates these. The writer bypasses ABCI, and nodes state-sync
// to the tip rather than replaying genesis-range blocks. The consequence, to
// be explicit, is that a future full-history validation from genesis would
// reject them. That is already true of every transaction this writer emits:
// signAndMarshal signs with the migration key and then overwrites Signer with
// the entity's real wallet, so the signature provably does not recover to the
// Signer it carries. Genesis-range transactions are authorized by
// genesis_migration_address / genesis_migration_end_height in the genesis
// file, not by per-transaction signature validity, and an unsigned pool create
// is honest about being one of them.
//
// deadline_block_height is 0 for the same reason: no deadline was ever signed
// over, and a fabricated one would only make an unvalidatable transaction look
// more validatable than it is.
func (w *Writer) synthesizeRewardPoolTxs(pools []rewardPool) ([][]byte, error) {
	txs := make([][]byte, 0, len(pools))
	for _, p := range pools {
		if len(p.Authorities) == 0 {
			// A pool with no authority can never attest for any reward
			// attached to it, so emitting it would migrate a dead row.
			return nil, fmt.Errorf("reward pool %s has no authorities", p.RewardsManagerPubkey)
		}
		stx := &corev1.SignedTransaction{
			RequestId: uuid.NewString(),
			Transaction: &corev1.SignedTransaction_RewardPool{
				RewardPool: &corev1.RewardPoolMessage{
					Body: &corev1.RewardPoolBody{
						DeadlineBlockHeight: 0,
						Action: &corev1.RewardPoolBody_Create{
							Create: &corev1.CreateRewardPool{
								RewardsManagerPubkey: p.RewardsManagerPubkey,
								Authorities:          p.Authorities,
							},
						},
					},
				},
			},
		}
		txBytes, err := proto.Marshal(stx)
		if err != nil {
			return nil, fmt.Errorf("marshal create reward pool %s: %w", p.RewardsManagerPubkey, err)
		}
		txs = append(txs, txBytes)
	}
	return txs, nil
}
