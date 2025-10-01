package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	corev1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	"github.com/AudiusProject/audiusd/pkg/rewards"
	abcitypes "github.com/cometbft/cometbft/abci/types"
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
		RecipientEthAddress:       ethRecipientAddress,
		Amount:                    amountUint,
		RewardID:                  rewardID,
		Specifier:                 specifier,
		AntiAbuseOracleEthAddress: oracleAddress,
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
	rewardMsg := signedTx.GetReward()
	if rewardMsg == nil {
		return fmt.Errorf("%w: reward message is nil", ErrRewardMessageValidation)
	}

	switch action := rewardMsg.Action.(type) {
	case *corev1.RewardMessage_Create:
		return s.validateCreateReward(ctx, action.Create, blockHeight)
	case *corev1.RewardMessage_Delete:
		return s.validateDeleteReward(ctx, action.Delete, blockHeight)
	default:
		return fmt.Errorf("%w: unsupported reward action type", ErrRewardMessageValidation)
	}
}

func (s *Server) validateCreateReward(_ context.Context, createReward *corev1.CreateReward, blockHeight int64) error {
	signatureData := common.CreateDeterministicCreateRewardData(createReward)
	_, err := s.validateRewardSignature(blockHeight, createReward.Signature, createReward.DeadlineBlockHeight, signatureData)
	if err != nil {
		return fmt.Errorf("create reward validation failed: %w", err)
	}
	return nil
}

func (s *Server) validateDeleteReward(ctx context.Context, deleteReward *corev1.DeleteReward, blockHeight int64) error {
	signatureData := common.CreateDeterministicDeleteRewardData(deleteReward)
	signer, err := s.validateRewardSignature(blockHeight, deleteReward.Signature, deleteReward.DeadlineBlockHeight, signatureData)
	if err != nil {
		return fmt.Errorf("delete reward validation failed: %w", err)
	}

	existingReward, err := s.db.GetReward(ctx, deleteReward.Address)
	if err != nil {
		return fmt.Errorf("failed to get existing reward for validation: %w", err)
	}

	authorized := false
	for _, auth := range existingReward.ClaimAuthorities {
		if strings.EqualFold(auth, signer) {
			authorized = true
			break
		}
	}
	if !authorized {
		return fmt.Errorf("%w: signer %s not authorized to delete reward %s", ErrRewardUnauthorized, signer, deleteReward.Address)
	}

	return nil
}

// validateRewardSignature validates the signature and expiry for reward messages
func (s *Server) validateRewardSignature(currentHeight int64, signature string, deadlineHeight int64, signatureData string) (string, error) {
	// Check expiry
	if currentHeight > deadlineHeight {
		return "", fmt.Errorf("%w: current height %d > deadline %d", ErrRewardExpired, currentHeight, deadlineHeight)
	}

	// Convert hex data to bytes for signing
	dataBytes, err := hex.DecodeString(signatureData)
	if err != nil {
		return "", fmt.Errorf("%w: invalid hex data: %v", ErrRewardSignatureInvalid, err)
	}

	// Recover signer from signature
	_, signer, err := common.EthRecover(signature, dataBytes)
	if err != nil {
		return "", fmt.Errorf("%w: failed to recover signer: %v", ErrRewardSignatureInvalid, err)
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

func (s *Server) finalizeRewards(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, messageIndex int64, rewardMsg *corev1.RewardMessage, sender string) error {
	if rewardMsg == nil {
		return fmt.Errorf("tx: %s, message index: %d, reward message not found", txhash, messageIndex)
	}

	switch action := rewardMsg.Action.(type) {
	case *corev1.RewardMessage_Create:
		if err := s.finalizeCreateReward(ctx, req, txhash, messageIndex, action.Create, sender); err != nil {
			return errors.Join(ErrRewardMessageFinalization, err)
		}
		return nil

	case *corev1.RewardMessage_Delete:
		if err := s.finalizeDeleteReward(ctx, req, txhash, messageIndex, action.Delete, sender); err != nil {
			return errors.Join(ErrRewardMessageFinalization, err)
		}
		return nil

	default:
		return fmt.Errorf("tx: %s, message index: %d, unsupported reward action type", txhash, messageIndex)
	}
}

func (s *Server) finalizeCreateReward(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, messageIndex int64, createReward *corev1.CreateReward, sender string) error {
	// Validate signature and get signer
	signatureData := common.CreateDeterministicCreateRewardData(createReward)
	signer, err := s.validateRewardSignature(req.Height, createReward.Signature, createReward.DeadlineBlockHeight, signatureData)
	if err != nil {
		return fmt.Errorf("create reward signature validation failed: %w", err)
	}

	// Generate deterministic address for the new reward
	txhashBytes, err := common.HexToBytes(txhash)
	if err != nil {
		return fmt.Errorf("invalid txhash: %w", err)
	}
	rewardAddress := common.CreateAddress(txhashBytes, s.config.GenesisFile.ChainID, req.Height, messageIndex, "")

	// Convert claim authorities to string array
	claimAuthorities := make([]string, len(createReward.ClaimAuthorities))
	for i, auth := range createReward.ClaimAuthorities {
		claimAuthorities[i] = auth.Address
	}

	// Marshal the raw message
	rawMessage, err := proto.Marshal(createReward)
	if err != nil {
		return fmt.Errorf("failed to marshal create reward message: %w", err)
	}

	qtx := s.getDb()
	if err := qtx.InsertCoreReward(ctx, db.InsertCoreRewardParams{
		TxHash:           txhash,
		Index:            messageIndex,
		Address:          rewardAddress,
		Sender:           signer, // Use verified signer instead of passed sender
		RewardID:         createReward.RewardId,
		Name:             createReward.Name,
		Amount:           int64(createReward.Amount),
		ClaimAuthorities: claimAuthorities,
		RawMessage:       rawMessage,
		BlockHeight:      req.Height,
	}); err != nil {
		return fmt.Errorf("failed to insert reward: %w", err)
	}

	return nil
}

func (s *Server) finalizeDeleteReward(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, messageIndex int64, deleteReward *corev1.DeleteReward, sender string) error {
	// Validate signature and get signer
	signatureData := common.CreateDeterministicDeleteRewardData(deleteReward)
	signer, err := s.validateRewardSignature(req.Height, deleteReward.Signature, deleteReward.DeadlineBlockHeight, signatureData)
	if err != nil {
		return fmt.Errorf("delete reward signature validation failed: %w", err)
	}

	// Verify signer is authorized to delete this reward
	existingReward, err := s.getDb().GetReward(ctx, deleteReward.Address)
	if err != nil {
		return fmt.Errorf("failed to get existing reward: %w", err)
	}

	// Check if signer is in the claim authorities (case insensitive)
	authorized := false
	for _, auth := range existingReward.ClaimAuthorities {
		if strings.EqualFold(auth, signer) {
			authorized = true
			break
		}
	}
	if !authorized {
		return fmt.Errorf("signer %s not authorized to delete reward %s", signer, deleteReward.Address)
	}

	qtx := s.getDb()
	if err := qtx.DeleteCoreReward(ctx, deleteReward.Address); err != nil {
		return fmt.Errorf("failed to delete reward: %w", err)
	}

	return nil
}
