package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sort"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// Rewards are migrated by rebuilding them from the old chain's TABLE state,
// not by replaying its transactions.
//
// An earlier version scanned ~66M rows of core_transactions looking for reward
// transactions by protobuf wire tag, in order to replay the original signed
// bytes verbatim. That is unnecessary: once the transactions are signed fresh,
// the original bytes have no role, and core_reward_pools + core_rewards already
// carry every field a new transaction needs. Two SELECTs against two small
// tables replace the scan, along with the chunking, statement timeouts, and
// xmin-horizon care that scanning a live production validator required.
//
// Columns deliberately not read: address, id, index, tx_hash, sender,
// raw_message, block_height, and the timestamps. The chain derives all of them
// when it processes the transaction. `sender` in particular becomes whichever
// key signs here, which is intended — the alternative is carrying a signature
// over a digest these transactions do not have.

// convertedRewardDeadlineHeight bounds how long a signature stays admissible.
// That exists to stop a captured live submission being replayed later; a
// migrated transaction is written directly into a block at a height the writer
// chooses, so the only real requirement is that it not be born expired at any
// height the genesis range can reach. Production genesis output is a few
// thousand blocks, so this is six orders of magnitude of headroom while still
// being a bounded value rather than a sentinel.
const convertedRewardDeadlineHeight = int64(1_000_000_000)

// rewardPool is one row of the old chain's core_reward_pools.
type rewardPool struct {
	RewardsManagerPubkey string
	Authorities          []string
}

// rewardRow is one row of the old chain's core_rewards, reduced to the fields
// a modern CreateReward carries.
type rewardRow struct {
	RewardID             string
	Name                 string
	Amount               int64
	RewardsManagerPubkey string
}

// rewardPlan is the decision of what to emit, made before anything is signed so
// that a disagreement between the pool set and the key material stops the run
// rather than producing a partially-migrated chain.
type rewardPlan struct {
	// pools to emit a CreateRewardPool for, in a stable order.
	pools []rewardPool
	// remap sends a phantom pool's rewards to the reward manager their mint
	// actually has. Keyed by the phantom's rewards_manager_pubkey.
	remap map[string]string
	// dropped names the phantom pools, for logging.
	dropped []string
}

// writeRewards rebuilds the old chain's reward pools and rewards as freshly
// signed transactions.
func (w *Writer) writeRewards(ctx context.Context) error {
	if w.cfg.CoreDSN == "" {
		return fmt.Errorf("rewards need the old chain's database: pass --core-dsn, " +
			"or --skip-rewards to migrate without them")
	}

	keys, err := loadLaunchpadKeys(w.cfg.LaunchpadMintsFile)
	if err != nil {
		return err
	}
	if keys == nil {
		return fmt.Errorf("rewards need launchpad key material: set %s and %s plus a mint list, "+
			"or --skip-rewards to migrate without them", launchpadSecretEnvVar, launchpadRotatedSecretEnvVar)
	}

	conn, err := connectOldCore(ctx, w.cfg.CoreDSN)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	pools, err := readRewardPools(ctx, conn)
	if err != nil {
		return err
	}
	rewards, err := readRewards(ctx, conn)
	if err != nil {
		return err
	}
	w.logger.Info("read reward state from the old chain",
		zap.Int("pools", len(pools)),
		zap.Int("rewards", len(rewards)),
		zap.Int("mints_derived", len(keys.mints)))

	plan, err := planRewardPools(pools, keys)
	if err != nil {
		return err
	}
	for _, rm := range plan.dropped {
		w.logger.Warn("dropping pool whose reward manager has no Solana account; "+
			"its rewards move to the reward manager their mint actually has",
			zap.String("rewards_manager_pubkey", rm),
			zap.String("moved_to", plan.remap[rm]))
	}

	// Pools first: a reward names its pool, and validateCreateReward requires
	// that pool to already exist.
	for _, p := range plan.pools {
		txBytes, err := w.synthesizeRewardPoolTx(p, keys)
		if err != nil {
			return err
		}
		if err := w.addTx(ctx, txBytes); err != nil {
			return fmt.Errorf("emit create reward pool %s: %w", p.RewardsManagerPubkey, err)
		}
	}

	emitted := 0
	for _, r := range rewards {
		rm := r.RewardsManagerPubkey
		if to, ok := plan.remap[rm]; ok {
			rm = to
		}
		txBytes, err := w.synthesizeRewardTx(r, rm, keys, plan)
		if err != nil {
			return err
		}
		if err := w.addTx(ctx, txBytes); err != nil {
			return fmt.Errorf("emit reward %s: %w", r.RewardID, err)
		}
		emitted++
	}

	w.logger.Info("migrated rewards",
		zap.Int("pools_created", len(plan.pools)),
		zap.Int("pools_dropped", len(plan.dropped)),
		zap.Int("rewards", emitted))
	return nil
}

// planRewardPools decides which pools are real and where a phantom's rewards go.
//
// Every reward manager on Solana was initialized once, under whichever launchpad
// secret was live at the time, and cannot move. A pool whose RM derives only
// from the ROTATED secret *for a mint that also has an ORIGINAL-generation pool*
// therefore names an account that does not exist: it is the artifact of reward
// creation re-deriving the RM after the secret changed. Its rewards belong to
// the mint's real reward manager.
//
// The qualifier matters. A rotated-generation RM is not phantom on its own — a
// mint launched after the rotation legitimately has one, and it does exist on
// Solana. Only the duplicate is wrong.
func planRewardPools(pools []rewardPool, keys *launchpadKeys) (*rewardPlan, error) {
	// Index the original-generation pool per mint, so a rotated-generation pool
	// can find the RM it should have used.
	originalByMint := map[string]string{}
	for _, p := range pools {
		id, ok := keys.identityForRM(p.RewardsManagerPubkey)
		if !ok {
			return nil, fmt.Errorf("no launchpad mint derives reward manager %s under either secret: "+
				"the mint list is incomplete, or neither %s nor %s is a secret this pool came from",
				p.RewardsManagerPubkey, launchpadSecretEnvVar, launchpadRotatedSecretEnvVar)
		}
		if id.generation == secretOriginal {
			originalByMint[id.mint] = p.RewardsManagerPubkey
		}
	}

	plan := &rewardPlan{remap: map[string]string{}}
	for _, p := range pools {
		id, _ := keys.identityForRM(p.RewardsManagerPubkey)
		if id.generation == secretRotated {
			if real, ok := originalByMint[id.mint]; ok {
				plan.remap[p.RewardsManagerPubkey] = real
				plan.dropped = append(plan.dropped, p.RewardsManagerPubkey)
				continue
			}
		}
		if len(p.Authorities) == 0 {
			// A pool with no authority can never attest for any reward under
			// it, so emitting it would migrate a row nothing can use.
			return nil, fmt.Errorf("reward pool %s has no authorities", p.RewardsManagerPubkey)
		}
		if _, _, err := keys.authorityKeyFor(p.Authorities); err != nil {
			return nil, fmt.Errorf("pool %s: %w", p.RewardsManagerPubkey, err)
		}
		plan.pools = append(plan.pools, p)
	}

	sort.Slice(plan.pools, func(i, j int) bool {
		return plan.pools[i].RewardsManagerPubkey < plan.pools[j].RewardsManagerPubkey
	})
	sort.Strings(plan.dropped)
	return plan, nil
}

// poolFor returns the planned pool a reward manager refers to.
func (p *rewardPlan) poolFor(rm string) (rewardPool, bool) {
	for _, pool := range p.pools {
		if pool.RewardsManagerPubkey == rm {
			return pool, true
		}
	}
	return rewardPool{}, false
}

// synthesizeRewardPoolTx builds a fully valid CreateRewardPool.
//
// Both signatures are real. The envelope is signed by one of the pool's own
// authorities, satisfying validateCreateRewardPool's signer-membership check,
// and rm_owner_signature is ed25519 over the same body by the reward manager
// keypair — the key the launchpad derived when it created that account on
// Solana. Neither is an exemption or a placeholder.
func (w *Writer) synthesizeRewardPoolTx(p rewardPool, keys *launchpadKeys) ([]byte, error) {
	authKey, authority, err := keys.authorityKeyFor(p.Authorities)
	if err != nil {
		return nil, fmt.Errorf("pool %s: %w", p.RewardsManagerPubkey, err)
	}
	rmKey, err := keys.rmKeyForPool(p.RewardsManagerPubkey)
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
	sig, err := common.ProtoSign(authKey, body)
	if err != nil {
		return nil, fmt.Errorf("sign create reward pool %s with authority %s: %w", p.RewardsManagerPubkey, authority, err)
	}
	bodyBytes, err := common.ProtoSignableBytes(body)
	if err != nil {
		return nil, fmt.Errorf("marshal reward pool body for rm signature: %w", err)
	}
	return proto.Marshal(&corev1.SignedTransaction{
		RequestId: uuid.NewString(),
		Transaction: &corev1.SignedTransaction_RewardPool{
			RewardPool: &corev1.RewardPoolMessage{
				Body:             body,
				Signature:        sig,
				RmOwnerSignature: ed25519.Sign(rmKey, bodyBytes),
			},
		},
	})
}

// synthesizeRewardTx builds a CreateReward signed by an authority of the pool
// it names. claim_authorities are not carried: authorities live on the pool
// now, and the pool already holds them at their current values.
func (w *Writer) synthesizeRewardTx(r rewardRow, rm string, keys *launchpadKeys, plan *rewardPlan) ([]byte, error) {
	pool, ok := plan.poolFor(rm)
	if !ok {
		return nil, fmt.Errorf("reward %s names reward manager %s, which no migrated pool covers", r.RewardID, rm)
	}
	authKey, authority, err := keys.authorityKeyFor(pool.Authorities)
	if err != nil {
		return nil, fmt.Errorf("reward %s: %w", r.RewardID, err)
	}

	body := &corev1.RewardBody{
		DeadlineBlockHeight: convertedRewardDeadlineHeight,
		Action: &corev1.RewardBody_Create{
			Create: &corev1.CreateReward{
				RewardId:             r.RewardID,
				Name:                 r.Name,
				Amount:               uint64(r.Amount),
				RewardsManagerPubkey: rm,
			},
		},
	}
	sig, err := common.ProtoSign(authKey, body)
	if err != nil {
		return nil, fmt.Errorf("sign reward %s with authority %s: %w", r.RewardID, authority, err)
	}
	return proto.Marshal(&corev1.SignedTransaction{
		RequestId: uuid.NewString(),
		Transaction: &corev1.SignedTransaction_Reward{
			Reward: &corev1.RewardMessage{Body: body, Signature: sig},
		},
	})
}

// readRewardPools reads core_reward_pools.
//
// The rows are the authority on what pools exist, and the transactions are not:
// of the pools on the production chain, only one was ever created by a
// transaction. The rest were materialized by a migration that DERIVES pools
// from core_rewards, which leaves the pool set reconstructible only by
// re-running that migration against a table it also rewrites. Reading the rows
// and emitting real creates is what retires it.
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

// readRewards reads core_rewards, ordered so a run is reproducible.
//
// Rows with no reward manager are refused rather than skipped. A NULL there
// means the row predates the pool model and was never resolved to one, and
// silently dropping rewards is the failure this migration exists to avoid.
func readRewards(ctx context.Context, conn *pgx.Conn) ([]rewardRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT reward_id, name, amount, rewards_manager_pubkey
		FROM core_rewards
		ORDER BY block_height, index, reward_id
	`)
	if err != nil {
		return nil, fmt.Errorf("read core_rewards: %w", err)
	}
	defer rows.Close()

	var out []rewardRow
	for rows.Next() {
		var r rewardRow
		var rm *string
		if err := rows.Scan(&r.RewardID, &r.Name, &r.Amount, &rm); err != nil {
			return nil, fmt.Errorf("scan reward row: %w", err)
		}
		if rm == nil {
			return nil, fmt.Errorf("reward %s has no rewards_manager_pubkey; it cannot be bound to a pool", r.RewardID)
		}
		r.RewardsManagerPubkey = *rm
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read reward rows: %w", err)
	}
	return out, nil
}

// connectOldCore opens a deliberately-constrained read-only connection to the
// old chain's database.
//
// This runs against a live production validator, so the connection is set up to
// be incapable of the damage a migration read could otherwise do: it cannot
// write, it cannot sit idle inside a transaction, and no single statement can
// run away. application_name makes the session recognisable in pg_stat_activity
// if someone needs to find or kill it.
//
// The timeouts are generous relative to what these queries need — two SELECTs
// against two small tables — because they exist as a backstop, not a budget.
func connectOldCore(ctx context.Context, dsn string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse core dsn: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = "genesis-writer-rewards"
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["statement_timeout"] = "120000"
	cfg.RuntimeParams["idle_in_transaction_session_timeout"] = "30000"
	// Never let the migration wait behind another session's lock.
	cfg.RuntimeParams["lock_timeout"] = "5000"

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to old core db: %w", err)
	}
	return conn, nil
}
