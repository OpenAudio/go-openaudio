package server

import (
	"context"
	"errors"
	"fmt"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"
)

// Reward state effects, factored out of the finalize path.
//
// The functions in the first half of this file are the writes a reward
// transaction performs on core_reward_pools / core_rewards, with no
// authorization or signature logic attached. finalizeCreateRewardPool,
// finalizeCreateReward and finalizeLegacyCreateReward run their checks and
// then call these; ProjectMigrationRewardState calls them directly. Keeping
// the writes in one place is what stops the genesis writer and FinalizeBlock
// from drifting into two different notions of what a reward transaction does
// to the tables — the same arrangement ProjectMigrationAuthState uses for the
// core_auth_* tables.

// insertRewardPoolIfAbsent writes the pool row and leaves an existing row
// untouched.
//
// Idempotence is load-bearing on both callers. At finalize time, same-RM
// in-block collisions can pass proposal validation because each transaction is
// checked against pre-block state; in the genesis writer, a re-run replays the
// same synthesized creates over tables a previous run already populated.
func insertRewardPoolIfAbsent(ctx context.Context, q *db.Queries, rewardsManagerPubkey string, authorities []string) error {
	if _, err := q.GetRewardPool(ctx, rewardsManagerPubkey); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("failed to check pool existence: %w", err)
	}
	return q.InsertRewardPool(ctx, db.InsertRewardPoolParams{
		RewardsManagerPubkey: rewardsManagerPubkey,
		Authorities:          rewards.CanonicalAuthorities(authorities),
	})
}

// rewardRow is one core_rewards insert. The reward's address is derived here
// rather than by the caller so that the derivation — which binds the row to
// the chain it was replayed onto, not the chain it came from — has a single
// definition.
type rewardRow struct {
	chainID      string
	txhash       string
	height       int64
	messageIndex int64
	signer       string
	rewardID     string
	name         string
	amount       uint64
	rmPubkey     pgtype.Text
	rawMessage   []byte
}

func insertRewardRow(ctx context.Context, q *db.Queries, r rewardRow) error {
	txhashBytes, err := common.HexToBytes(r.txhash)
	if err != nil {
		return fmt.Errorf("invalid txhash: %w", err)
	}
	return q.InsertCoreReward(ctx, db.InsertCoreRewardParams{
		TxHash:               r.txhash,
		Index:                r.messageIndex,
		Address:              common.CreateAddress(txhashBytes, r.chainID, r.height, r.messageIndex, ""),
		Sender:               r.signer,
		RewardID:             r.rewardID,
		Name:                 r.name,
		Amount:               int64(r.amount),
		RewardsManagerPubkey: r.rmPubkey,
		RawMessage:           r.rawMessage,
		BlockHeight:          r.height,
	})
}

// resolveLegacyRewardRM maps a legacy reward's inline claim authorities to a
// real reward manager through the launchpad_authority_rm seed, which the
// schema migration populates. ok is false when no authority matches any known
// launchpad key: such a reward has no pool and is inserted with a NULL
// rewards_manager_pubkey, matching what the migration backfill does for the
// same rows.
func resolveLegacyRewardRM(ctx context.Context, q *db.Queries, canonicalAuthorities []string) (rm string, ok bool, err error) {
	if len(canonicalAuthorities) == 0 {
		return "", false, nil
	}
	rm, err = q.GetLaunchpadRMByAuthority(ctx, canonicalAuthorities)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("failed to resolve launchpad RM: %w", err)
	default:
		return rm, true, nil
	}
}

// legacyClaimAuthorities returns a legacy reward's inline claim authorities in
// canonical (trimmed, lowercased, deduped, sorted) form.
func legacyClaimAuthorities(cr *corev1.LegacyCreateReward) []string {
	addrs := make([]string, 0, len(cr.ClaimAuthorities))
	for _, auth := range cr.ClaimAuthorities {
		addrs = append(addrs, auth.Address)
	}
	return rewards.CanonicalAuthorities(addrs)
}

// ProjectMigrationRewardState applies a replayed reward transaction's state
// effects to core_reward_pools / core_rewards, using the caller's queries
// handle so the writes join whatever transaction it owns.
//
// It exists for genesis-writer, which inserts blocks straight into postgres
// without going through consensus: FinalizeBlock never executes those
// transactions, so unless the writer projects the reward state itself the new
// chain starts with the reward tables empty — the rewards are present as
// transactions and as nothing else, and every claim against them fails.
//
// Transactions that are not reward transactions return handled=false and are
// the caller's cue to move on.
//
// # What this deliberately does not do
//
// It does not re-run admission control. The signature, deadline and
// pool-authorization gates are the checks the SOURCE chain already ran when it
// accepted these transactions; re-running them here against a different
// chain's block heights and a different point on the pool-authority timeline
// would reject valid history. That is not hypothetical. The genesis writer
// creates each pool at its FINAL authority set, after the rotations that moved
// two production pools off their original launchpad-derived keys; 442 of the
// 471 production rewards were signed by the pre-rotation key and so are not
// authorized under the pool state they are being replayed into.
// checkPoolAuthorization would drop all of them.
//
// The signer is still recovered, because core_rewards.sender records it, and a
// recovery failure is reported as a skip rather than silently substituted.
//
// skipped reports that the projection understood the transaction and declined
// it, with reason describing why. A migration replays state the source system
// already accepted, so any skip is a defect the caller should surface.
func ProjectMigrationRewardState(
	ctx context.Context,
	q *db.Queries,
	stx *corev1.SignedTransaction,
	txhash string,
	chainID string,
	height int64,
) (handled bool, skipped bool, reason string, err error) {
	switch tx := stx.GetTransaction().(type) {
	case *corev1.SignedTransaction_RewardPool:
		skipped, reason, err = projectRewardPoolTx(ctx, q, tx.RewardPool)
		return true, skipped, reason, err
	case *corev1.SignedTransaction_Reward:
		skipped, reason, err = projectRewardTx(ctx, q, tx.Reward, txhash, chainID, height)
		return true, skipped, reason, err
	default:
		return false, false, "", nil
	}
}

func projectRewardPoolTx(ctx context.Context, q *db.Queries, envelope *corev1.RewardPoolMessage) (skipped bool, reason string, err error) {
	if envelope.GetBody() == nil {
		return true, "reward pool message has no body", nil
	}
	switch action := envelope.GetBody().GetAction().(type) {
	case *corev1.RewardPoolBody_Create:
		msg := action.Create
		if err := validateRewardsManagerPubkeyShape(msg.GetRewardsManagerPubkey()); err != nil {
			return true, err.Error(), nil
		}
		if err := validateAuthorityList(msg.GetAuthorities()); err != nil {
			return true, err.Error(), nil
		}
		if err := insertRewardPoolIfAbsent(ctx, q, msg.GetRewardsManagerPubkey(), msg.GetAuthorities()); err != nil {
			return false, "", err
		}
		return false, "", nil

	case *corev1.RewardPoolBody_SetAuthorities:
		// Reached only by the --core-cmt-home path, which replays the old
		// chain's pool transactions verbatim. The --core-dsn path synthesizes
		// creates at the final authority set instead, which makes the
		// rotations redundant and drops them.
		msg := action.SetAuthorities
		if err := validateRewardsManagerPubkeyShape(msg.GetRewardsManagerPubkey()); err != nil {
			return true, err.Error(), nil
		}
		if err := validateAuthorityList(msg.GetAuthorities()); err != nil {
			return true, err.Error(), nil
		}
		if _, err := q.GetRewardPool(ctx, msg.GetRewardsManagerPubkey()); errors.Is(err, pgx.ErrNoRows) {
			return true, fmt.Sprintf("pool %s not found", msg.GetRewardsManagerPubkey()), nil
		} else if err != nil {
			return false, "", fmt.Errorf("failed to load pool %s: %w", msg.GetRewardsManagerPubkey(), err)
		}
		if err := q.UpdateRewardPoolAuthorities(ctx, db.UpdateRewardPoolAuthoritiesParams{
			RewardsManagerPubkey: msg.GetRewardsManagerPubkey(),
			Authorities:          rewards.CanonicalAuthorities(msg.GetAuthorities()),
		}); err != nil {
			return false, "", fmt.Errorf("failed to rotate pool authorities: %w", err)
		}
		return false, "", nil

	default:
		return true, "unsupported reward pool action type", nil
	}
}

func projectRewardTx(
	ctx context.Context,
	q *db.Queries,
	envelope *corev1.RewardMessage,
	txhash string,
	chainID string,
	height int64,
) (skipped bool, reason string, err error) {
	// Single-message reward transactions, matching finalizeRewardTransaction.
	const messageIndex = int64(0)

	if envelope.GetBody() == nil {
		legacy, err := tryParseLegacyReward(envelope)
		if err != nil {
			return true, fmt.Sprintf("legacy reward decode: %v", err), nil
		}
		if legacy == nil {
			return true, "reward message has no body and no legacy action", nil
		}
		return projectLegacyRewardTx(ctx, q, legacy, txhash, chainID, height, messageIndex)
	}

	create, ok := envelope.GetBody().GetAction().(*corev1.RewardBody_Create)
	if !ok {
		// DeleteReward removes a row the source chain already removed, so it
		// has no replayed state to project: the writer only ever sees the
		// rewards that survived. Anything else is a shape we do not know.
		if _, isDelete := envelope.GetBody().GetAction().(*corev1.RewardBody_Delete); isDelete {
			return false, "", nil
		}
		return true, "unsupported reward action type", nil
	}

	signer, err := common.ProtoRecover(envelope.GetBody(), envelope.GetSignature())
	if err != nil {
		return true, fmt.Sprintf("signer recovery: %v", err), nil
	}
	rawMessage, err := proto.Marshal(create.Create)
	if err != nil {
		return false, "", fmt.Errorf("failed to marshal create reward message: %w", err)
	}
	if err := insertRewardRow(ctx, q, rewardRow{
		chainID:      chainID,
		txhash:       txhash,
		height:       height,
		messageIndex: messageIndex,
		signer:       signer,
		rewardID:     create.Create.GetRewardId(),
		name:         create.Create.GetName(),
		amount:       create.Create.GetAmount(),
		rmPubkey:     pgtype.Text{String: create.Create.GetRewardsManagerPubkey(), Valid: true},
		rawMessage:   rawMessage,
	}); err != nil {
		return false, "", fmt.Errorf("failed to insert reward: %w", err)
	}
	return false, "", nil
}

// projectLegacyRewardTx binds a legacy-shape reward to its pool WITHOUT
// unioning the reward's inline claim authorities into that pool.
//
// finalizeLegacyCreateReward does union them, and must: on a node block-syncing
// from genesis it is the only thing that materializes a pool at all, and it has
// to arrive at the same authority set the schema migration's UNION-based
// backfill produced on an in-place-upgraded node.
//
// The genesis writer is in the opposite position. It creates every pool up
// front from the old chain's own core_reward_pools rows, so the pool already
// exists at its final authority set by the time any reward is projected, and
// unioning would REVERSE the rotations. The two production pools that were
// rotated were rotated away from launchpad keys derived from a leaked
// deterministic secret; folding those keys back in through the rewards that
// still carry them would hand attestation authority back to the compromised
// keys and undo the reason pools exist.
func projectLegacyRewardTx(
	ctx context.Context,
	q *db.Queries,
	legacy *corev1.LegacyRewardMessage,
	txhash string,
	chainID string,
	height int64,
	messageIndex int64,
) (skipped bool, reason string, err error) {
	create, ok := legacy.GetAction().(*corev1.LegacyRewardMessage_Create)
	if !ok {
		if _, isDelete := legacy.GetAction().(*corev1.LegacyRewardMessage_Delete); isDelete {
			return false, "", nil
		}
		return true, "unknown legacy reward action", nil
	}
	cr := create.Create

	signer, err := common.LegacyRecoverCreateReward(cr)
	if err != nil {
		return true, fmt.Sprintf("legacy signer recovery: %v", err), nil
	}

	var rmPubkey pgtype.Text
	rm, ok, err := resolveLegacyRewardRM(ctx, q, legacyClaimAuthorities(cr))
	if err != nil {
		return false, "", err
	}
	if ok {
		rmPubkey = pgtype.Text{String: rm, Valid: true}
	}

	rawMessage, err := proto.Marshal(cr)
	if err != nil {
		return false, "", fmt.Errorf("failed to marshal legacy create reward: %w", err)
	}
	if err := insertRewardRow(ctx, q, rewardRow{
		chainID:      chainID,
		txhash:       txhash,
		height:       height,
		messageIndex: messageIndex,
		signer:       signer,
		rewardID:     cr.GetRewardId(),
		name:         cr.GetName(),
		amount:       cr.GetAmount(),
		rmPubkey:     rmPubkey,
		rawMessage:   rawMessage,
	}); err != nil {
		return false, "", fmt.Errorf("failed to insert legacy reward: %w", err)
	}
	return false, "", nil
}
