package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"strings"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/server"
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

	var poolTxBytes [][]byte
	var rewardTxs []scannedRewardTx
	var falsePositives int
	start := time.Now()

	// One statement per window, each its own implicit transaction. A single
	// scan over the whole table would pin the xmin horizon for its duration
	// and block autovacuum cleanup across the database.
	for lo := minBlock; lo <= maxBlock; lo += chunk {
		hi := lo + chunk
		rows, err := conn.Query(ctx, `
			SELECT transaction, tx_hash, block_id
			FROM core_transactions
			WHERE block_id >= $1 AND block_id < $2
			  AND (position($3::bytea in transaction) > 0
			    OR position($4::bytea in transaction) > 0)
			ORDER BY block_id, index
		`, lo, hi, rewardTag, rewardPoolTag)
		if err != nil {
			return fmt.Errorf("scan core_transactions blocks [%d,%d): %w", lo, hi, err)
		}

		var window []scannedRewardTx
		for rows.Next() {
			var t scannedRewardTx
			if err := rows.Scan(&t.txBytes, &t.txHash, &t.blockHeight); err != nil {
				rows.Close()
				return fmt.Errorf("scan reward tx row: %w", err)
			}
			window = append(window, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read reward rows [%d,%d): %w", lo, hi, err)
		}

		// Confirm by type. The byte search is a prefilter, not a decision.
		for _, t := range window {
			var stx corev1.SignedTransaction
			if err := proto.Unmarshal(t.txBytes, &stx); err != nil {
				falsePositives++
				continue
			}
			switch stx.Transaction.(type) {
			case *corev1.SignedTransaction_RewardPool:
				poolTxBytes = append(poolTxBytes, t.txBytes)
			case *corev1.SignedTransaction_Reward:
				rewardTxs = append(rewardTxs, t)
			default:
				falsePositives++
			}
		}

		if lo/chunk%20 == 0 {
			w.logger.Info("scan progress",
				zap.Int64("block", hi),
				zap.Int64("max_block", maxBlock),
				zap.Int("pools", len(poolTxBytes)),
				zap.Int("rewards", len(rewardTxs)))
		}
	}

	pools, err := readRewardPools(ctx, conn)
	if err != nil {
		return err
	}
	bindings, err := readRewardBindings(ctx, conn)
	if err != nil {
		return err
	}

	w.logger.Info("found reward txs",
		zap.Int("scanned_pool_txs", len(poolTxBytes)),
		zap.Int("rewards", len(rewardTxs)),
		zap.Int("pools_in_state", len(pools)),
		zap.Int("reward_bindings_in_state", len(bindings)),
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

	// Report key coverage before doing anything with it, so a dry run is a
	// complete pre-flight: an operator finds out a key is missing here rather
	// than partway through a multi-hour write.
	missing := w.missingPoolSigners(pools)
	for _, m := range missing {
		w.logger.Error("no reward signing key for any authority of pool",
			zap.String("rewards_manager_pubkey", m.RewardsManagerPubkey),
			zap.Strings("authorities", m.Authorities))
	}

	if w.cfg.CoreScanDryRun {
		w.logger.Info("dry run: emitting nothing. Reconcile these against the old chain " +
			"(select count(*) from core_reward_pools / core_rewards) before a real run.")
		return nil
	}

	// Fail rather than fall back to replaying the legacy bytes. A fallback
	// would turn a missing key into a chain that quietly carries the artifacts
	// this conversion exists to remove, discovered — if ever — long after the
	// migration. Rewards silently degrading is the failure this whole change
	// is chasing; --skip-rewards is the way to opt out on purpose.
	if len(missing) > 0 {
		return fmt.Errorf("no reward signing key for %d of %d pools (see the errors above); "+
			"set %s or --reward-signing-keys-file, or pass --skip-rewards",
			len(missing), len(pools), rewardSigningKeysEnvVar)
	}

	poolCreateTxs, err := w.synthesizeRewardPoolTxs(pools)
	if err != nil {
		return err
	}

	rewardOut, stats, err := w.convertRewardTxs(rewardTxs, pools, bindings)
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
	for i, txBytes := range rewardOut {
		if err := w.addTx(ctx, txBytes); err != nil {
			return fmt.Errorf("emit reward tx %d: %w", i, err)
		}
	}

	w.logger.Info("emitted reward txs",
		zap.Int("pool_creates", len(poolCreateTxs)),
		zap.Int("dropped_pool_txs", len(poolTxBytes)),
		zap.Int("rewards_total", len(rewardOut)),
		zap.Int("rewards_converted_from_legacy", stats.converted),
		zap.Int("rewards_already_modern", stats.alreadyModern),
		zap.Int("rewards_dropped_no_state", stats.droppedNoBinding))
	return nil
}

// scannedRewardTx is a reward transaction as found on the old chain, carrying
// the identity needed to look up the reward manager it resolved to.
type scannedRewardTx struct {
	txBytes     []byte
	txHash      string
	blockHeight int64
}

// rewardBindingKey identifies one reward row. tx_hash alone is not enough: on
// the production chain two byte-identical reward transactions were each
// committed in two different blocks, so the same hash appears twice with two
// distinct rewards behind it.
type rewardBindingKey struct {
	txHash      string
	blockHeight int64
}

// readRewardBindings reads the reward manager each reward resolved to on the
// old chain.
//
// core_rewards.rewards_manager_pubkey IS the resolved binding — the schema
// migration already walked claim_authorities through launchpad_authority_rm to
// produce it. Reading it reproduces the old chain's state by construction
// rather than by recomputing a derivation against a seed table, and it is the
// reason the converted transactions need no launchpad mapping at all.
func readRewardBindings(ctx context.Context, conn *pgx.Conn) (map[rewardBindingKey]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT tx_hash, block_height, rewards_manager_pubkey
		 FROM core_rewards WHERE rewards_manager_pubkey IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("read core_rewards bindings: %w", err)
	}
	defer rows.Close()

	out := map[rewardBindingKey]string{}
	for rows.Next() {
		var k rewardBindingKey
		var rm string
		if err := rows.Scan(&k.txHash, &k.blockHeight, &rm); err != nil {
			return nil, fmt.Errorf("scan reward binding: %w", err)
		}
		out[k] = rm
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read reward binding rows: %w", err)
	}
	return out, nil
}

// conversionStats is the reconciliation breakdown for a run.
type conversionStats struct {
	converted        int
	alreadyModern    int
	droppedNoBinding int
}

// convertRewardTxs turns every scanned reward transaction into a modern,
// validly-signed CreateReward, so the new chain carries no legacy reward
// artifacts at all.
//
// Legacy-shape transactions are rebuilt as CreateReward and signed fresh with
// the pool authority's key. Signing fresh rather than carrying the old
// signature across is what makes the conversion possible: the legacy scheme
// signs a sha256 over a pipe-delimited string that INCLUDES claim_authorities,
// while the modern envelope signs deterministic-proto bytes of RewardBody.
// Carrying the old signature would not fail loudly — secp256k1 recovery
// against the wrong digest returns a valid-looking but different address —
// it would silently write a wrong core_rewards.sender, which is served over
// the public reward API as GetRewardResponse.sender.
//
// Transactions already in the modern shape are passed through byte-for-byte.
// Their signers are the pools' current authorities already, so they are valid
// as they stand; re-signing them would fabricate transactions to no purpose
// and would change hashes that today still match the old chain.
func (w *Writer) convertRewardTxs(
	scanned []scannedRewardTx,
	pools []rewardPool,
	bindings map[rewardBindingKey]string,
) ([][]byte, conversionStats, error) {
	var stats conversionStats
	out := make([][]byte, 0, len(scanned))

	for _, t := range scanned {
		var stx corev1.SignedTransaction
		if err := proto.Unmarshal(t.txBytes, &stx); err != nil {
			return nil, stats, fmt.Errorf("decode reward tx %s: %w", t.txHash, err)
		}
		envelope := stx.GetReward()
		if envelope == nil {
			return nil, stats, fmt.Errorf("reward tx %s is not a reward message", t.txHash)
		}

		rm, hasBinding := bindings[rewardBindingKey{txHash: t.txHash, blockHeight: t.blockHeight}]
		if !hasBinding {
			// No core_rewards row means the old chain does not carry this
			// reward in state — it was deleted, or never resolved to a pool.
			// Replaying it would invent state the source does not have.
			stats.droppedNoBinding++
			w.logger.Warn("dropping reward tx with no reward state on the old chain",
				zap.String("tx_hash", t.txHash),
				zap.Int64("old_block_height", t.blockHeight))
			continue
		}

		if envelope.GetBody() != nil {
			// Already modern. Confirm the pool it names matches the binding
			// the old chain recorded before passing the bytes through.
			create := envelope.GetBody().GetCreate()
			if create == nil {
				return nil, stats, fmt.Errorf("reward tx %s has a body but is not a CreateReward", t.txHash)
			}
			if create.GetRewardsManagerPubkey() != rm {
				return nil, stats, fmt.Errorf("reward tx %s names pool %s but the old chain bound it to %s",
					t.txHash, create.GetRewardsManagerPubkey(), rm)
			}
			out = append(out, t.txBytes)
			stats.alreadyModern++
			continue
		}

		legacy, err := server.TryParseLegacyReward(envelope)
		if err != nil {
			return nil, stats, fmt.Errorf("decode legacy reward tx %s: %w", t.txHash, err)
		}
		if legacy == nil {
			return nil, stats, fmt.Errorf("reward tx %s has neither a body nor a legacy action", t.txHash)
		}
		create, ok := legacy.GetAction().(*corev1.LegacyRewardMessage_Create)
		if !ok {
			return nil, stats, fmt.Errorf("reward tx %s is a legacy action this conversion does not cover", t.txHash)
		}

		converted, err := w.convertLegacyReward(create.Create, rm, pools)
		if err != nil {
			return nil, stats, fmt.Errorf("convert reward tx %s: %w", t.txHash, err)
		}
		out = append(out, converted)
		stats.converted++
	}
	return out, stats, nil
}

// convertedRewardDeadlineHeight is the deadline the converted transactions
// carry.
//
// deadline_block_height bounds how long a signature stays admissible, which
// exists to stop a captured live submission being replayed later. A migrated
// transaction has no such window — it is written directly into a block at a
// height the writer chooses — so the only real requirement is that it must not
// be born expired at any height the genesis range can reach. Production genesis
// output is a few thousand blocks; this is six orders of magnitude above that,
// so the transactions stay admissible under any plausible growth of the
// migrated range while still being a bounded value rather than a sentinel.
const convertedRewardDeadlineHeight = int64(1_000_000_000)

// convertLegacyReward rebuilds a legacy reward as a modern CreateReward signed
// by an authority of its pool.
//
// claim_authorities is dropped rather than mapped: authorities live on the
// pool now, and the pool already holds them at their post-rotation values.
// This is the same union that the projection deliberately suppresses to keep
// the leaked-secret keys out of the pools — conversion makes that structural
// instead of something the projection has to remember not to do.
func (w *Writer) convertLegacyReward(cr *corev1.LegacyCreateReward, rm string, pools []rewardPool) ([]byte, error) {
	key, authority, err := w.signerForPool(rm, pools)
	if err != nil {
		return nil, err
	}
	body := &corev1.RewardBody{
		DeadlineBlockHeight: convertedRewardDeadlineHeight,
		Action: &corev1.RewardBody_Create{
			Create: &corev1.CreateReward{
				RewardId:             cr.GetRewardId(),
				Name:                 cr.GetName(),
				Amount:               cr.GetAmount(),
				RewardsManagerPubkey: rm,
			},
		},
	}
	sig, err := common.ProtoSign(key, body)
	if err != nil {
		return nil, fmt.Errorf("sign converted reward with authority %s: %w", authority, err)
	}
	return proto.Marshal(&corev1.SignedTransaction{
		RequestId: uuid.NewString(),
		Transaction: &corev1.SignedTransaction_Reward{
			Reward: &corev1.RewardMessage{Body: body, Signature: sig},
		},
	})
}

// signerForPool returns the key for the first authority of the named pool that
// the operator supplied a key for, along with that authority's address.
func (w *Writer) signerForPool(rm string, pools []rewardPool) (*ecdsa.PrivateKey, string, error) {
	for _, p := range pools {
		if p.RewardsManagerPubkey != rm {
			continue
		}
		for _, a := range p.Authorities {
			if key, ok := w.cfg.RewardSigningKeys[strings.ToLower(strings.TrimSpace(a))]; ok {
				return key, a, nil
			}
		}
		return nil, "", fmt.Errorf("no reward signing key for any authority of pool %s (%v)", rm, p.Authorities)
	}
	return nil, "", fmt.Errorf("reward references pool %s, which the old chain has no row for", rm)
}

// missingPoolSigners returns the pools no supplied key can sign for.
func (w *Writer) missingPoolSigners(pools []rewardPool) []rewardPool {
	var missing []rewardPool
	for _, p := range pools {
		if _, _, err := w.signerForPool(p.RewardsManagerPubkey, pools); err != nil {
			missing = append(missing, p)
		}
	}
	return missing
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
// The envelope is signed by one of the pool's own initial authorities, using
// the operator-supplied key for that address. validateCreateRewardPool
// requires the recovered secp256k1 signer to be a member of msg.authorities,
// and it is — so this half of the check passes for real, not by exemption.
//
// rm_owner_signature is left EMPTY, and unlike the envelope signature it
// cannot be filled. It is ed25519 over the body by the reward manager keypair,
// which only the launchpad's deterministic secret can produce; being unable to
// produce it is the security property, not an obstacle, and it is not
// something to forge. So these transactions are authorized by a genuine
// authority and still fall short of full validateCreateRewardPool acceptance.
//
// Nothing re-validates them. The writer bypasses ABCI, and nodes state-sync to
// the tip rather than replaying genesis-range blocks. The residual consequence,
// stated plainly: a future full-history validation from genesis would reject
// the four pool creates on the missing rm_owner_signature. Genesis-range
// transactions are authorized by genesis_migration_address /
// genesis_migration_end_height rather than by per-transaction signature
// validity — signAndMarshal already overwrites Signer after signing, so no
// migration transaction recovers to the Signer it carries — and these are
// strictly closer to valid than the rest of the migrated range, not further.
//
// deadline_block_height matches the converted rewards; see
// convertedRewardDeadlineHeight.
func (w *Writer) synthesizeRewardPoolTxs(pools []rewardPool) ([][]byte, error) {
	txs := make([][]byte, 0, len(pools))
	for _, p := range pools {
		if len(p.Authorities) == 0 {
			// A pool with no authority can never attest for any reward
			// attached to it, so emitting it would migrate a dead row.
			return nil, fmt.Errorf("reward pool %s has no authorities", p.RewardsManagerPubkey)
		}
		key, authority, err := w.signerForPool(p.RewardsManagerPubkey, pools)
		if err != nil {
			return nil, err
		}
		body := &corev1.RewardPoolBody{
			DeadlineBlockHeight: convertedRewardDeadlineHeight,
			Action: &corev1.RewardPoolBody_Create{
				Create: &corev1.CreateRewardPool{
					RewardsManagerPubkey: p.RewardsManagerPubkey,
					Authorities:          p.Authorities,
				},
			},
		}
		sig, err := common.ProtoSign(key, body)
		if err != nil {
			return nil, fmt.Errorf("sign create reward pool %s with authority %s: %w", p.RewardsManagerPubkey, authority, err)
		}
		txBytes, err := proto.Marshal(&corev1.SignedTransaction{
			RequestId: uuid.NewString(),
			Transaction: &corev1.SignedTransaction_RewardPool{
				RewardPool: &corev1.RewardPoolMessage{Body: body, Signature: sig},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal create reward pool %s: %w", p.RewardsManagerPubkey, err)
		}
		txs = append(txs, txBytes)
	}
	return txs, nil
}
