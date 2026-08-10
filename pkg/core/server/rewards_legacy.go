package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"
)

// Legacy reward wire-compat layer.
//
// Pre-pool-rollout, RewardMessage was a oneof of CreateReward / DeleteReward
// with deadline + signature embedded inside each action. The current
// body+signature envelope is wire-incompatible: legacy bytes have no
// field at tag 1 (the new body) so the new proto unmarshals them with
// Body == nil.
//
// This file handles the case where a legacy-shaped reward tx is encountered
// during block-sync-from-genesis. We re-decode the unknown fields as
// LegacyRewardMessage, recover the signer using the legacy signing scheme
// (sha256 over a pipe-delimited canonical string — see
// pkg/common/legacy_reward_signing.go), and bind the reward to a real-RM
// pool by looking up any one of its inline claim_authorities in the
// launchpad_authority_rm seed (matching the migration backfill exactly).
// For DeleteReward we re-check authorization against the existing reward's
// pool authorities. Rewards whose authorities don't match any launchpad
// entry are inserted with NULL rewards_manager_pubkey.
//
// Live-traffic note: post-rollout, no client should produce legacy bytes —
// the SDK and API repo only emit the new envelope. This path is dormant
// after the migration window and only activates for replay.

// TryParseLegacyReward exposes tryParseLegacyReward to genesis-writer, which
// converts the legacy reward transactions it replays into the modern shape and
// so has to decode them first. Reusing this rather than re-implementing the
// unknown-fields round-trip keeps one definition of what "legacy-shaped" means.
func TryParseLegacyReward(rm *corev1.RewardMessage) (*corev1.LegacyRewardMessage, error) {
	return tryParseLegacyReward(rm)
}

// tryParseLegacyReward inspects a RewardMessage that decoded with Body == nil
// and re-attempts to decode its bytes as the legacy oneof shape. Returns nil
// if no legacy action is present (e.g., the bytes really were empty / from a
// future incompatible version).
func tryParseLegacyReward(rm *corev1.RewardMessage) (*corev1.LegacyRewardMessage, error) {
	if rm == nil || rm.Body != nil {
		return nil, nil
	}
	// proto-go preserves unknown fields by default; round-tripping the new
	// RewardMessage through Marshal recovers the original tag 1000/1001 bytes.
	raw, err := proto.Marshal(rm)
	if err != nil {
		return nil, fmt.Errorf("legacy reward: re-marshal: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var legacy corev1.LegacyRewardMessage
	if err := proto.Unmarshal(raw, &legacy); err != nil {
		return nil, fmt.Errorf("legacy reward: unmarshal: %w", err)
	}
	if legacy.Action == nil {
		return nil, nil
	}
	return &legacy, nil
}

// finalizeLegacyRewardTransaction applies a legacy-shape reward tx. For
// CreateReward, the reward's RM is resolved by looking up any one of its
// inline claim_authorities in the launchpad_authority_rm mapping (the
// schema migration populates this table); rewards whose authorities
// don't match any known launchpad authority are inserted with NULL
// rewards_manager_pubkey. For DeleteReward, the row is removed after
// re-checking authorization against the existing reward's pool
// authorities.
func (s *Server) finalizeLegacyRewardTransaction(ctx context.Context, req *abcitypes.FinalizeBlockRequest, legacy *corev1.LegacyRewardMessage, txhash string, messageIndex int64) error {
	switch a := legacy.Action.(type) {
	case *corev1.LegacyRewardMessage_Create:
		signer, err := s.recoverLegacyCreateRewardSigner(req.Height, a.Create)
		if err != nil {
			return errors.Join(ErrRewardMessageFinalization, err)
		}
		if err := s.finalizeLegacyCreateReward(ctx, req, txhash, messageIndex, a.Create, signer); err != nil {
			return errors.Join(ErrRewardMessageFinalization, err)
		}
		return nil
	case *corev1.LegacyRewardMessage_Delete:
		signer, err := s.recoverLegacyDeleteRewardSigner(req.Height, a.Delete)
		if err != nil {
			return errors.Join(ErrRewardMessageFinalization, err)
		}
		if err := s.finalizeLegacyDeleteReward(ctx, a.Delete, signer); err != nil {
			return errors.Join(ErrRewardMessageFinalization, err)
		}
		return nil
	default:
		return fmt.Errorf("tx: %s, message index: %d, unknown legacy reward action", txhash, messageIndex)
	}
}

func (s *Server) recoverLegacyCreateRewardSigner(currentHeight int64, cr *corev1.LegacyCreateReward) (string, error) {
	if currentHeight > cr.DeadlineBlockHeight {
		return "", fmt.Errorf("%w: current height %d > deadline %d", ErrRewardExpired, currentHeight, cr.DeadlineBlockHeight)
	}
	signer, err := common.LegacyRecoverCreateReward(cr)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRewardSignatureInvalid, err)
	}
	return signer, nil
}

func (s *Server) recoverLegacyDeleteRewardSigner(currentHeight int64, dr *corev1.LegacyDeleteReward) (string, error) {
	if currentHeight > dr.DeadlineBlockHeight {
		return "", fmt.Errorf("%w: current height %d > deadline %d", ErrRewardExpired, currentHeight, dr.DeadlineBlockHeight)
	}
	signer, err := common.LegacyRecoverDeleteReward(dr)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRewardSignatureInvalid, err)
	}
	return signer, nil
}

func (s *Server) finalizeLegacyCreateReward(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, messageIndex int64, cr *corev1.LegacyCreateReward, signer string) error {
	qtx := s.getDb()

	// Resolve the reward's RM by looking for a launchpad-derived authority
	// among the message's inline claim_authorities. The launchpad_authority_rm
	// table (populated by the schema migration) maps each per-mint claim
	// authority (lowercased eth hex) to the Solana reward manager state
	// account that mint's rewards live under. This produces the SAME
	// (RM, authorities) state the migration backfill produced for
	// pre-migration rows, so historical replay and the migration agree
	// on the resulting apphash.
	canonicalAuthorities := legacyClaimAuthorities(cr)

	var rmPubkey pgtype.Text
	rm, ok, err := resolveLegacyRewardRM(ctx, qtx, canonicalAuthorities)
	if err != nil {
		return err
	}
	if ok {
		// Upsert (UNION) the pool so successive legacy replays of
		// rewards under the same RM accumulate into the same authority
		// set the migration's UNION-based backfill produces.
		//
		// The genesis writer's projection deliberately does NOT do this;
		// see projectLegacyRewardTx for why the two differ.
		if err := qtx.UpsertLegacyReplayRewardPool(ctx, db.UpsertLegacyReplayRewardPoolParams{
			RewardsManagerPubkey: rm,
			Authorities:          canonicalAuthorities,
		}); err != nil {
			return fmt.Errorf("failed to upsert reward pool for legacy replay: %w", err)
		}
		rmPubkey = pgtype.Text{String: rm, Valid: true}
	}

	rawMessage, err := proto.Marshal(cr)
	if err != nil {
		return fmt.Errorf("failed to marshal legacy create reward: %w", err)
	}
	if err := insertRewardRow(ctx, qtx, rewardRow{
		chainID:      s.config.GenesisFile.ChainID,
		txhash:       txhash,
		height:       req.Height,
		messageIndex: messageIndex,
		signer:       signer,
		rewardID:     cr.RewardId,
		name:         cr.Name,
		amount:       cr.Amount,
		rmPubkey:     rmPubkey,
		rawMessage:   rawMessage,
	}); err != nil {
		return fmt.Errorf("failed to insert legacy reward: %w", err)
	}
	return nil
}

func (s *Server) finalizeLegacyDeleteReward(ctx context.Context, dr *corev1.LegacyDeleteReward, signer string) error {
	qtx := s.getDb()
	existingReward, err := qtx.GetReward(ctx, dr.Address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: reward %s not found", ErrRewardMessageValidation, dr.Address)
		}
		return fmt.Errorf("legacy delete: failed to get reward: %w", err)
	}
	if !slices.Contains(existingReward.ClaimAuthorities, strings.ToLower(strings.TrimSpace(signer))) {
		return fmt.Errorf("%w: legacy signer %s no longer authorized to delete reward %s", ErrRewardUnauthorized, signer, dr.Address)
	}
	if err := qtx.DeleteCoreReward(ctx, dr.Address); err != nil {
		return fmt.Errorf("failed to delete legacy reward: %w", err)
	}
	return nil
}
