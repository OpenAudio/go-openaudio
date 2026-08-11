package server

import (
	"context"
	"fmt"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"
)

// ProjectMigrationRewardState applies the state effects a reward transaction
// would have had at finalize time.
//
// It exists for the genesis writer, which inserts blocks straight into postgres
// and never runs them through ABCI. Without this, a migrated chain would carry
// reward transactions in core_transactions while core_reward_pools and
// core_rewards stayed empty — and nothing downstream would repair that, because
// the bootstrap node treats those blocks as already committed and other nodes
// state-sync from its tables. The same reasoning already applies to the auth
// projection alongside this one.
//
// It deliberately does NOT re-run admission control. Signature, deadline and
// authorization were established when the writer built these transactions, and
// re-checking them here would fail for reasons that do not apply to a block
// being written rather than proposed. What it must reproduce exactly is the
// ROWS finalize writes — most importantly core_rewards.address, which is
// derived from (txhash, chain id, height, message index) and so can only be
// computed once the transaction has a place in a block.
//
// Returns handled=false for transactions that are not reward transactions, so
// the caller can pass every transaction in a block without pre-filtering.
func ProjectMigrationRewardState(
	ctx context.Context,
	q *db.Queries,
	stx *corev1.SignedTransaction,
	chainID string,
	height int64,
	messageIndex int64,
	txhash string,
) (handled bool, err error) {
	switch {
	case stx.GetRewardPool() != nil:
		return true, projectRewardPool(ctx, q, stx.GetRewardPool())
	case stx.GetReward() != nil:
		return true, projectReward(ctx, q, stx.GetReward(), chainID, height, messageIndex, txhash)
	default:
		return false, nil
	}
}

// projectRewardPool inserts the row finalizeCreateRewardPool would.
//
// Only Create is projected. The genesis writer creates every pool at its final
// authority set, so a rotation has nothing left to do; if one is ever emitted,
// failing here is better than silently diverging from the transaction log.
func projectRewardPool(ctx context.Context, q *db.Queries, envelope *corev1.RewardPoolMessage) error {
	if envelope.Body == nil {
		return fmt.Errorf("reward pool message has no body")
	}
	create := envelope.Body.GetCreate()
	if create == nil {
		return fmt.Errorf("reward pool projection covers Create only; got %T", envelope.Body.Action)
	}
	if err := q.InsertRewardPool(ctx, db.InsertRewardPoolParams{
		RewardsManagerPubkey: create.RewardsManagerPubkey,
		Authorities:          rewards.CanonicalAuthorities(create.Authorities),
	}); err != nil {
		return fmt.Errorf("insert reward pool %s: %w", create.RewardsManagerPubkey, err)
	}
	return nil
}

// projectReward inserts the row finalizeCreateReward would, including the
// derived address and the recovered sender.
//
// The sender is recovered rather than assumed: core_rewards.sender is
// API-visible, and it must be the address that actually signed, not the one we
// believe signed.
func projectReward(
	ctx context.Context,
	q *db.Queries,
	msg *corev1.RewardMessage,
	chainID string,
	height int64,
	messageIndex int64,
	txhash string,
) error {
	if msg.Body == nil {
		return fmt.Errorf("reward message has no body")
	}
	create := msg.Body.GetCreate()
	if create == nil {
		return fmt.Errorf("reward projection covers Create only; got %T", msg.Body.Action)
	}

	signer, err := common.ProtoRecover(msg.Body, msg.Signature)
	if err != nil {
		return fmt.Errorf("recover reward signer for tx %s: %w", txhash, err)
	}

	txhashBytes, err := common.HexToBytes(txhash)
	if err != nil {
		return fmt.Errorf("invalid txhash %s: %w", txhash, err)
	}
	rawMessage, err := proto.Marshal(create)
	if err != nil {
		return fmt.Errorf("marshal create reward: %w", err)
	}

	if err := q.InsertCoreReward(ctx, db.InsertCoreRewardParams{
		Address:              common.CreateAddress(txhashBytes, chainID, height, messageIndex, ""),
		TxHash:               txhash,
		Index:                messageIndex,
		Sender:               signer,
		RewardID:             create.RewardId,
		Name:                 create.Name,
		Amount:               int64(create.Amount),
		RewardsManagerPubkey: pgtype.Text{String: create.RewardsManagerPubkey, Valid: true},
		RawMessage:           rawMessage,
		BlockHeight:          height,
	}); err != nil {
		return fmt.Errorf("insert reward %s: %w", create.RewardId, err)
	}
	return nil
}
