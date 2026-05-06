package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"
	"google.golang.org/protobuf/proto"
)

func (s *Server) getRewards(c echo.Context) error {
	return c.JSON(http.StatusOK, s.rewards.Rewards)
}

func (s *Server) getRewardAttestation(c echo.Context) error {
	ethRecipientAddress := c.QueryParam("eth_recipient_address")
	if ethRecipientAddress == "" {
		return c.JSON(http.StatusBadRequest, "eth_recipient_address is required")
	}
	rewardID := c.QueryParam("reward_id")
	if rewardID == "" {
		return c.JSON(http.StatusBadRequest, "reward_id is required")
	}
	specifier := c.QueryParam("specifier")
	if specifier == "" {
		return c.JSON(http.StatusBadRequest, "specifier is required")
	}
	oracleAddress := c.QueryParam("oracle_address")
	if oracleAddress == "" {
		return c.JSON(http.StatusBadRequest, "oracle_address is required")
	}
	signature := c.QueryParam("signature")
	if signature == "" {
		return c.JSON(http.StatusBadRequest, "signature is required")
	}
	amount := c.QueryParam("amount")
	if amount == "" {
		return c.JSON(http.StatusBadRequest, "amount is required")
	}
	amountUint, err := strconv.ParseUint(amount, 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, "amount is invalid")
	}

	claim := rewards.RewardClaim{
		RecipientEthAddress: ethRecipientAddress,
		Amount:              amountUint,
		RewardID:            rewardID,
		Specifier:           specifier,
		ClaimAuthority:      oracleAddress,
	}

	err = s.rewards.Validate(claim)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	err = s.rewards.Authenticate(claim, signature)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, err.Error())
	}

	_, attestation, err := s.rewards.Attest(claim)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	res := map[string]any{
		"owner":       s.rewards.EthereumAddress,
		"attestation": attestation,
	}
	return c.JSON(http.StatusOK, res)
}

var (
	ErrRewardMessageValidation   = errors.New("reward message validation failed")
	ErrRewardMessageFinalization = errors.New("reward message finalization failed")
	ErrRewardSignatureInvalid    = errors.New("reward signature invalid")
	ErrRewardExpired             = errors.New("reward transaction expired")
	ErrRewardUnauthorized        = errors.New("reward transaction unauthorized")
)

func (s *Server) isValidRewardTransaction(ctx context.Context, signedTx *corev1.SignedTransaction, blockHeight int64) error {
	envelope := signedTx.GetReward()
	if envelope == nil {
		return fmt.Errorf("%w: reward message is nil", ErrRewardMessageValidation)
	}
	if envelope.Body == nil {
		// Legacy wire-format detected (pre-pool-rollout shape with deadline +
		// signature embedded in CreateReward / DeleteReward at tags 1000/1001).
		//
		// We REJECT legacy here — at CheckTx and ProcessProposal — because
		// legacy CreateReward is inherently permissionless: it carries no
		// pool_address and the old signing scheme had no membership check on
		// claim_authorities. Allowing live legacy txs through validation would
		// reopen the exact exploit class this PR is closing (an attacker
		// crafts legacy bytes, picks any reward_id / amount / claim_authorities,
		// and bypasses the pool gate).
		//
		// The corresponding code path in finalizeRewards still APPLIES legacy
		// txs — that's intentional, for block-sync-from-genesis replay of
		// already-committed historical blocks. Block sync only invokes
		// FinalizeBlock; it does not re-run CheckTx or ProcessProposal. So
		// "reject at validate, accept at finalize" gives us correct historical
		// replay without admitting any new legacy traffic.
		if legacy, err := tryParseLegacyReward(envelope); err == nil && legacy != nil {
			return fmt.Errorf("%w: legacy reward wire format is not accepted for new transactions; clients must use the body+signature envelope", ErrRewardMessageValidation)
		}
		return fmt.Errorf("%w: reward message body is nil", ErrRewardMessageValidation)
	}

	signer, err := s.recoverDeadlinedSigner(blockHeight, envelope.Body.DeadlineBlockHeight, envelope.Body, envelope.Signature)
	if err != nil {
		return fmt.Errorf("reward validation failed: %w", err)
	}

	switch action := envelope.Body.Action.(type) {
	case *corev1.RewardBody_Create:
		return s.validateCreateReward(ctx, action.Create, signer)
	case *corev1.RewardBody_Delete:
		return s.validateDeleteReward(ctx, action.Delete, signer)
	default:
		return fmt.Errorf("%w: unsupported reward action type", ErrRewardMessageValidation)
	}
}

func (s *Server) validateCreateReward(ctx context.Context, createReward *corev1.CreateReward, signer string) error {
	// New CreateReward requires a first-class pool. The pool is identified
	// by the Solana reward manager pubkey, and only pool members can issue
	// rewards under that RM. Without this binding, a member of one pool
	// could issue rewards under any other pool's RM and inherit its
	// attestation rights — which is exactly what PR3's per-RM gate is
	// designed to prevent.
	//
	// The legacy permissionless path (inline claim_authorities, no RM) is
	// preserved only for historical replay via the wire-compat layer; new
	// live txs must specify rewards_manager_pubkey.
	if err := validateRewardsManagerPubkey(createReward.RewardsManagerPubkey); err != nil {
		return err
	}
	return s.checkPoolAuthorization(ctx, s.db, createReward.RewardsManagerPubkey, signer)
}

func (s *Server) validateDeleteReward(ctx context.Context, deleteReward *corev1.DeleteReward, signer string) error {
	existingReward, err := s.db.GetReward(ctx, deleteReward.Address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: reward %s not found", ErrRewardMessageValidation, deleteReward.Address)
		}
		return fmt.Errorf("failed to get existing reward for validation: %w", err)
	}
	if !contains(existingReward.ClaimAuthorities, signer) {
		return fmt.Errorf("%w: signer %s not authorized to delete reward %s", ErrRewardUnauthorized, signer, deleteReward.Address)
	}
	return nil
}

// recoverDeadlinedSigner enforces the message's deadline against the current
// block height and recovers the eth address that produced signature over
// the deterministic-protobuf marshaling of msg. msg is expected to be a
// signed body (RewardBody / RewardPoolBody) — there is no signature field
// on the body, so common.ProtoRecover marshals it as-is. Used by every
// reward / reward-pool validator and finalizer.
func (s *Server) recoverDeadlinedSigner(currentHeight, deadlineHeight int64, msg proto.Message, signature string) (string, error) {
	if currentHeight > deadlineHeight {
		return "", fmt.Errorf("%w: current height %d > deadline %d", ErrRewardExpired, currentHeight, deadlineHeight)
	}
	signer, err := common.ProtoRecover(msg, signature)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRewardSignatureInvalid, err)
	}
	return signer, nil
}

func (s *Server) finalizeRewardTransaction(ctx context.Context, req *abcitypes.FinalizeBlockRequest, rewardMsg *corev1.RewardMessage, txhash string, sender string) (proto.Message, error) {
	// Use messageIndex of 0 for single reward transactions
	err := s.finalizeRewards(ctx, req, txhash, 0, rewardMsg, sender)
	if err != nil {
		return nil, err
	}
	return rewardMsg, nil
}

func (s *Server) finalizeRewards(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, messageIndex int64, envelope *corev1.RewardMessage, sender string) error {
	if envelope == nil {
		return fmt.Errorf("tx: %s, message index: %d, reward message not found", txhash, messageIndex)
	}
	if envelope.Body == nil {
		// Legacy wire-format path; see rewards_legacy.go.
		legacy, err := tryParseLegacyReward(envelope)
		if err != nil {
			return errors.Join(ErrRewardMessageFinalization, err)
		}
		if legacy == nil {
			return fmt.Errorf("tx: %s, message index: %d, reward message body not found", txhash, messageIndex)
		}
		return s.finalizeLegacyRewardTransaction(ctx, req, legacy, txhash, messageIndex)
	}

	signer, err := s.recoverDeadlinedSigner(req.Height, envelope.Body.DeadlineBlockHeight, envelope.Body, envelope.Signature)
	if err != nil {
		return errors.Join(ErrRewardMessageFinalization, fmt.Errorf("signature validation failed: %w", err))
	}

	switch action := envelope.Body.Action.(type) {
	case *corev1.RewardBody_Create:
		if err := s.finalizeCreateReward(ctx, req, txhash, messageIndex, action.Create, signer); err != nil {
			return errors.Join(ErrRewardMessageFinalization, err)
		}
		return nil

	case *corev1.RewardBody_Delete:
		if err := s.finalizeDeleteReward(ctx, action.Delete, signer); err != nil {
			return errors.Join(ErrRewardMessageFinalization, err)
		}
		return nil

	default:
		return fmt.Errorf("tx: %s, message index: %d, unsupported reward action type", txhash, messageIndex)
	}
}

func (s *Server) finalizeCreateReward(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, messageIndex int64, createReward *corev1.CreateReward, signer string) error {
	// New CreateReward must reference an existing first-class pool. Re-check
	// authorization against post-prior-tx state — block ordering can rotate
	// the signer out between validate and finalize.
	qtx := s.getDb()
	if err := s.checkPoolAuthorization(ctx, qtx, createReward.RewardsManagerPubkey, signer); err != nil {
		return err
	}

	txhashBytes, err := common.HexToBytes(txhash)
	if err != nil {
		return fmt.Errorf("invalid txhash: %w", err)
	}
	rewardAddress := common.CreateAddress(txhashBytes, s.config.GenesisFile.ChainID, req.Height, messageIndex, "")

	rawMessage, err := proto.Marshal(createReward)
	if err != nil {
		return fmt.Errorf("failed to marshal create reward message: %w", err)
	}

	if err := qtx.InsertCoreReward(ctx, db.InsertCoreRewardParams{
		TxHash:      txhash,
		Index:       messageIndex,
		Address:     rewardAddress,
		Sender:      signer,
		RewardID:    createReward.RewardId,
		Name:        createReward.Name,
		Amount:      int64(createReward.Amount),
		RewardsManagerPubkey: pgtype.Text{String: createReward.RewardsManagerPubkey, Valid: true},
		RawMessage:  rawMessage,
		BlockHeight: req.Height,
	}); err != nil {
		return fmt.Errorf("failed to insert reward: %w", err)
	}

	return nil
}

func (s *Server) finalizeDeleteReward(ctx context.Context, deleteReward *corev1.DeleteReward, signer string) error {
	// Re-check authorization against post-prior-tx state — a sibling tx in
	// the same block can rotate the signer out of the reward's pool between
	// validate (pre-block state) and finalize (post-prior-tx state).
	qtx := s.getDb()
	existingReward, err := qtx.GetReward(ctx, deleteReward.Address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: reward %s not found", ErrRewardMessageValidation, deleteReward.Address)
		}
		return fmt.Errorf("failed to get reward at finalize: %w", err)
	}
	if !contains(existingReward.ClaimAuthorities, signer) {
		return fmt.Errorf("%w: signer %s no longer authorized to delete reward %s", ErrRewardUnauthorized, signer, deleteReward.Address)
	}
	if err := qtx.DeleteCoreReward(ctx, deleteReward.Address); err != nil {
		return fmt.Errorf("failed to delete reward: %w", err)
	}
	return nil
}
