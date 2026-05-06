package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/mr-tron/base58/base58"
	"google.golang.org/protobuf/proto"
)

// solanaPubkeyByteLen is the wire-level size of a Solana pubkey (32 bytes,
// base58-encoded). Used to validate that a rewards_manager_pubkey supplied
// to CreateRewardPool / CreateReward / SetRewardPoolAuthorities is the
// shape of an actual on-chain reward manager account, not an arbitrary
// caller-chosen string.
const solanaPubkeyByteLen = 32

// isValidRewardPoolTransaction is the entry point for both CheckTx and
// block validation. Signature + deadline live on the envelope; we recover
// the signer once here and pass it to per-action validators.
func (s *Server) isValidRewardPoolTransaction(ctx context.Context, signedTx *corev1.SignedTransaction, blockHeight int64) error {
	envelope := signedTx.GetRewardPool()
	if envelope == nil || envelope.Body == nil {
		return fmt.Errorf("%w: reward pool message body is nil", ErrRewardMessageValidation)
	}

	signer, err := s.recoverDeadlinedSigner(blockHeight, envelope.Body.DeadlineBlockHeight, envelope.Body, envelope.Signature)
	if err != nil {
		return fmt.Errorf("reward pool validation failed: %w", err)
	}

	switch action := envelope.Body.Action.(type) {
	case *corev1.RewardPoolBody_Create:
		return s.validateCreateRewardPool(ctx, action.Create, signer)
	case *corev1.RewardPoolBody_SetAuthorities:
		return s.validateSetRewardPoolAuthorities(ctx, action.SetAuthorities, signer)
	default:
		return fmt.Errorf("%w: unsupported reward pool action type", ErrRewardMessageValidation)
	}
}

// validateCreateRewardPool: pool is identified by the Solana reward manager
// pubkey (must be valid base58 32 bytes); signer must be in the initial
// authorities; the initial authority list must be non-empty and contain
// only valid eth addresses.
func (s *Server) validateCreateRewardPool(ctx context.Context, msg *corev1.CreateRewardPool, signer string) error {
	if err := validateRewardsManagerPubkey(msg.RewardsManagerPubkey); err != nil {
		return err
	}
	if err := validateAuthorityList(msg.Authorities); err != nil {
		return err
	}
	canonical := rewards.CanonicalAuthorities(msg.Authorities)
	if !contains(canonical, strings.ToLower(strings.TrimSpace(signer))) {
		return fmt.Errorf("%w: signer %s not in initial authorities", ErrRewardUnauthorized, signer)
	}
	if _, err := s.db.GetRewardPool(ctx, msg.RewardsManagerPubkey); err == nil {
		return fmt.Errorf("%w: pool %s already exists", ErrRewardMessageValidation, msg.RewardsManagerPubkey)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("failed to check pool existence: %w", err)
	}
	return nil
}

// validateAuthorityList enforces that an authority list is non-empty and
// every entry is a valid eth address. Without this, a current authority can
// rotate the pool to ["not-an-address"], which passes the canonicalization
// pipeline but leaves the pool with no key that can ever satisfy
// checkPoolAuthorization — every reward attached to the pool becomes
// permanently unclaimable.
func validateAuthorityList(addrs []string) error {
	if len(addrs) == 0 {
		return fmt.Errorf("%w: at least one authority is required", ErrRewardMessageValidation)
	}
	for _, a := range addrs {
		if !ethcommon.IsHexAddress(strings.TrimSpace(a)) {
			return fmt.Errorf("%w: %q is not a valid eth address", ErrRewardMessageValidation, a)
		}
	}
	return nil
}

// validateRewardsManagerPubkey checks the wire shape of a Solana reward
// manager pubkey: non-empty, base58-decodable, exactly 32 bytes.
//
// First-class pools must use a real RM pubkey because PR3's
// sender-attestation gate uses the same value to bind the pool↔RM. PR1's
// backfill resolves each existing reward row to a real RM via the
// launchpad_authority_rm mapping, so there are no synthetic-pool
// identifiers in production state to special-case here.
func validateRewardsManagerPubkey(pubkey string) error {
	if pubkey == "" {
		return fmt.Errorf("%w: rewards_manager_pubkey is required", ErrRewardMessageValidation)
	}
	if pubkey != strings.TrimSpace(pubkey) {
		return fmt.Errorf("%w: rewards_manager_pubkey must not have surrounding whitespace", ErrRewardMessageValidation)
	}
	bytes, err := base58.Decode(pubkey)
	if err != nil {
		return fmt.Errorf("%w: rewards_manager_pubkey is not valid base58: %v", ErrRewardMessageValidation, err)
	}
	if len(bytes) != solanaPubkeyByteLen {
		return fmt.Errorf("%w: rewards_manager_pubkey must decode to %d bytes; got %d", ErrRewardMessageValidation, solanaPubkeyByteLen, len(bytes))
	}
	return nil
}

// validateSetRewardPoolAuthorities: rewards_manager_pubkey must be a real
// Solana RM pubkey (defense-in-depth — every pool created via
// CreateRewardPool already passed this check, but enforcing it here too
// closes any historical or future path that might have inserted a
// non-RM-shaped pool key); signer must be in the *current* pool
// authorities; the new list must be non-empty and contain only valid eth
// addresses (otherwise the pool can be rotated into a permanently-orphaned
// state).
func (s *Server) validateSetRewardPoolAuthorities(ctx context.Context, msg *corev1.SetRewardPoolAuthorities, signer string) error {
	if err := validateRewardsManagerPubkey(msg.RewardsManagerPubkey); err != nil {
		return err
	}
	if err := validateAuthorityList(msg.Authorities); err != nil {
		return err
	}
	return s.checkPoolAuthorization(ctx, s.db, msg.RewardsManagerPubkey, signer)
}

// finalizeRewardPoolTransaction is invoked after a tx is included in a block.
// Signature was already verified at validate time; we re-recover here only
// because the finalize path is its own consensus boundary.
func (s *Server) finalizeRewardPoolTransaction(ctx context.Context, req *abcitypes.FinalizeBlockRequest, envelope *corev1.RewardPoolMessage, txhash string, messageIndex int64) (proto.Message, error) {
	if envelope == nil || envelope.Body == nil {
		return nil, fmt.Errorf("tx: %s, message index: %d, reward pool message body not found", txhash, messageIndex)
	}
	signer, err := s.recoverDeadlinedSigner(req.Height, envelope.Body.DeadlineBlockHeight, envelope.Body, envelope.Signature)
	if err != nil {
		return nil, errors.Join(ErrRewardMessageFinalization, fmt.Errorf("signer recovery: %w", err))
	}

	switch action := envelope.Body.Action.(type) {
	case *corev1.RewardPoolBody_Create:
		if err := s.finalizeCreateRewardPool(ctx, action.Create); err != nil {
			return nil, errors.Join(ErrRewardMessageFinalization, err)
		}
	case *corev1.RewardPoolBody_SetAuthorities:
		if err := s.finalizeSetRewardPoolAuthorities(ctx, action.SetAuthorities, signer); err != nil {
			return nil, errors.Join(ErrRewardMessageFinalization, err)
		}
	default:
		return nil, fmt.Errorf("tx: %s, message index: %d, unsupported reward pool action type", txhash, messageIndex)
	}
	return envelope, nil
}

// finalizeCreateRewardPool: pool address == rewards_manager_pubkey. Same-RM
// in-block collisions surface as a PK violation from InsertRewardPool, which
// fails the tx (block continues; no chain crash).
func (s *Server) finalizeCreateRewardPool(ctx context.Context, msg *corev1.CreateRewardPool) error {
	return s.getDb().InsertRewardPool(ctx, db.InsertRewardPoolParams{
		RewardsManagerPubkey: msg.RewardsManagerPubkey,
		Authorities: rewards.CanonicalAuthorities(msg.Authorities),
	})
}

// finalizeSetRewardPoolAuthorities re-checks signer authorization against
// post-prior-tx state via s.getDb(), guarding against same-block ordering
// where an earlier tx rotates the signer out before this one runs.
func (s *Server) finalizeSetRewardPoolAuthorities(ctx context.Context, msg *corev1.SetRewardPoolAuthorities, signer string) error {
	if err := s.checkPoolAuthorization(ctx, s.getDb(), msg.RewardsManagerPubkey, signer); err != nil {
		return err
	}
	return s.getDb().UpdateRewardPoolAuthorities(ctx, db.UpdateRewardPoolAuthoritiesParams{
		RewardsManagerPubkey: msg.RewardsManagerPubkey,
		Authorities: rewards.CanonicalAuthorities(msg.Authorities),
	})
}

// checkPoolAuthorization fetches the pool from the supplied queries handle
// (s.db at validate time, s.getDb() at finalize time) and verifies signer is
// in the current authority set. Used by both validate and finalize paths so
// the rule lives in one place.
func (s *Server) checkPoolAuthorization(ctx context.Context, q *db.Queries, rewardsManagerPubkey, signer string) error {
	pool, err := q.GetRewardPool(ctx, rewardsManagerPubkey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: pool %s not found", ErrRewardMessageValidation, rewardsManagerPubkey)
		}
		return fmt.Errorf("failed to load pool %s: %w", rewardsManagerPubkey, err)
	}
	if !contains(pool.Authorities, strings.ToLower(strings.TrimSpace(signer))) {
		return fmt.Errorf("%w: signer %s not authorized for pool %s", ErrRewardUnauthorized, signer, rewardsManagerPubkey)
	}
	return nil
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}
