package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
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

// ErrRewardPoolOwnerSignatureInvalid signals that the envelope's
// rm_owner_signature did not verify against the body's claimed RM pubkey.
var ErrRewardPoolOwnerSignatureInvalid = errors.New("reward pool owner signature invalid")

// verifyRewardPoolOwnerSignature checks that the supplied ed25519
// signature, produced by the keypair whose public key is rmPubkey,
// covers bodyBytes. Returns nil iff the signature is valid; otherwise
// returns a wrapped ErrRewardPoolOwnerSignatureInvalid.
//
// The verification key IS rmPubkey, decoded from base58. There is no
// trusted-key registry: the body claims an RM, the RM IS its ed25519
// verification key, and possession of the matching secret key proves
// authorization to register a pool for that RM. The launchpad relay
// derives both the keypair and the per-mint claim authority eth address
// from (launchpadDeterministicSecret, mint), so the launchpad can
// always produce this signature; an attacker who lacks the secret
// cannot.
//
// bodyBytes are the deterministic-protobuf marshaling of RewardPoolBody —
// the same bytes the secp256k1 envelope signature covers. This means
// fields present in the body, including deadline_block_height, are
// implicitly covered by both signatures, defeating stale-signature replay
// without a separate signed-string format.
func verifyRewardPoolOwnerSignature(rmPubkey string, bodyBytes, signature []byte) error {
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature length is %d, want %d", ErrRewardPoolOwnerSignatureInvalid, len(signature), ed25519.SignatureSize)
	}
	pubkeyBytes, err := base58.Decode(rmPubkey)
	if err != nil {
		return fmt.Errorf("%w: rewards_manager_pubkey is not valid base58: %v", ErrRewardPoolOwnerSignatureInvalid, err)
	}
	if len(pubkeyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: rewards_manager_pubkey decodes to %d bytes, want %d", ErrRewardPoolOwnerSignatureInvalid, len(pubkeyBytes), ed25519.PublicKeySize)
	}
	if !ed25519.Verify(ed25519.PublicKey(pubkeyBytes), bodyBytes, signature) {
		return ErrRewardPoolOwnerSignatureInvalid
	}
	return nil
}

// isValidRewardPoolTransaction is the entry point for both CheckTx and
// block validation. Signatures + deadline live on the envelope; we
// recover the secp256k1 signer once here, then dispatch to per-action
// validators that handle the rest (including the ed25519
// rm_owner_signature for Create).
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
		return s.validateCreateRewardPool(ctx, envelope, action.Create, signer)
	case *corev1.RewardPoolBody_SetAuthorities:
		return s.validateSetRewardPoolAuthorities(ctx, action.SetAuthorities, signer)
	default:
		return fmt.Errorf("%w: unsupported reward pool action type", ErrRewardMessageValidation)
	}
}

// validateCreateRewardPool runs two independent authorization checks
// alongside the structural validation:
//
//   - envelope.rm_owner_signature (ed25519, verified against
//     msg.rewards_manager_pubkey): proves the caller controls the
//     RM keypair. Defeats frontrunning by an observer who sees the RM
//     pubkey on Solana but lacks the launchpad's deterministic secret.
//   - envelope signer (secp256k1, recovered from envelope.signature) ∈
//     msg.authorities: keeps the "you can't create a pool you have no
//     membership in" property. Operationally the launchpad relay holds
//     both keys and uses the per-mint claim authority eth key as the
//     envelope signer and as the only initial authority.
//
// Both signatures cover the same body bytes — the secp256k1 path uses
// common.ProtoRecover (which marshals deterministically); the ed25519
// path verifies against common.ProtoSignableBytes(body) directly. body
// covers deadline_block_height and the action oneof, so stale-deadline
// replay is blocked for both signatures together. Cross-chain replay
// isn't a threat: each environment's launchpad uses a different
// deterministic secret, so the same rewards_manager_pubkey cannot exist
// on more than one chain.
func (s *Server) validateCreateRewardPool(ctx context.Context, envelope *corev1.RewardPoolMessage, msg *corev1.CreateRewardPool, signer string) error {
	if err := validateRewardsManagerPubkey(msg.RewardsManagerPubkey); err != nil {
		return err
	}
	if err := validateAuthorityList(msg.Authorities); err != nil {
		return err
	}
	bodyBytes, err := common.ProtoSignableBytes(envelope.Body)
	if err != nil {
		return fmt.Errorf("%w: marshal body for rm signature: %v", ErrRewardPoolOwnerSignatureInvalid, err)
	}
	if err := verifyRewardPoolOwnerSignature(msg.RewardsManagerPubkey, bodyBytes, envelope.RmOwnerSignature); err != nil {
		return err
	}
	canonical := rewards.CanonicalAuthorities(msg.Authorities)
	if !slices.Contains(canonical, strings.ToLower(strings.TrimSpace(signer))) {
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

// validateRewardsManagerPubkeyShape checks the wire shape of a Solana
// reward manager pubkey: non-empty, no surrounding whitespace, base58-
// decodable, exactly 32 bytes. Pure shape validation — no policy. Use
// from read paths and rotation paths where the AUDIO RM is a legitimate
// input that should round-trip cleanly (read paths return NotFound;
// sender-gate falls back to validator/AAO).
func validateRewardsManagerPubkeyShape(pubkey string) error {
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

// validateRewardsManagerPubkey is the WRITE-path validator: shape +
// the AUDIO reserved-RM denylist. Use from CreateRewardPool and
// CreateReward. Allowing a pool to be created for the AUDIO RM would
// shift AUDIO sender attestations to pool-controlled authorities,
// defeating the AUDIO trust model — AUDIO is intentionally governed by
// the network-wide validator/AAO trust set in senderGateForRM (the
// gate falls back to validator/AAO when the configured AUDIO RM has
// no pool).
//
// First-class pools must use a real RM pubkey because senderGateForRM
// uses the same value to bind the pool↔RM. The backfill resolves each
// existing reward row to a real RM via the launchpad_authority_rm
// mapping, so there are no synthetic-pool identifiers in production
// state to special-case here.
func validateRewardsManagerPubkey(pubkey string) error {
	if err := validateRewardsManagerPubkeyShape(pubkey); err != nil {
		return err
	}
	// Trim defensively on the configured side: if these per-env
	// constants ever start being populated from env vars or config,
	// surrounding whitespace would silently disable the check.
	if audioRM := strings.TrimSpace(config.AudioRewardsManagerPubkey()); audioRM != "" && pubkey == audioRM {
		return fmt.Errorf("%w: rewards_manager_pubkey is reserved (AUDIO); pools cannot be created for it", ErrRewardMessageValidation)
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
	// Shape-only here, not the AUDIO denylist: SetRewardPoolAuthorities
	// targets an existing pool, and AUDIO has no pool by construction
	// (validateCreateRewardPool refuses), so checkPoolAuthorization
	// surfaces the AUDIO case as the more accurate "pool not found"
	// rather than the create-time "is reserved" message.
	if err := validateRewardsManagerPubkeyShape(msg.RewardsManagerPubkey); err != nil {
		return err
	}
	if err := validateAuthorityList(msg.Authorities); err != nil {
		return err
	}
	return s.checkPoolAuthorization(ctx, s.db, msg.RewardsManagerPubkey, signer)
}

// finalizeRewardPoolTransaction is invoked after a tx is included in a block.
// Signatures were already verified at validate time; we re-check here as
// defense-in-depth because the finalize path is its own consensus boundary
// (block-sync replay does not re-run ProcessProposal / CheckTx).
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
		if err := s.finalizeCreateRewardPool(ctx, envelope, action.Create, signer); err != nil {
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
// in-block collisions can pass proposal validation because each tx is checked
// against pre-block state. Finalize against the in-progress block transaction
// and make later creates idempotent so duplicate inserts cannot poison commit.
//
// Re-validates the message shape at finalize time as defense-in-depth:
// block-sync replay calls FinalizeBlock without re-running ProcessProposal,
// so any malformed bytes that ever reached the chain would skip the
// validate-time checks. Repeating the same shape + signer-membership
// validation here keeps the post-replay state identical to what the live
// validate path produces.
func (s *Server) finalizeCreateRewardPool(ctx context.Context, envelope *corev1.RewardPoolMessage, msg *corev1.CreateRewardPool, signer string) error {
	if err := validateRewardsManagerPubkey(msg.RewardsManagerPubkey); err != nil {
		return err
	}
	if err := validateAuthorityList(msg.Authorities); err != nil {
		return err
	}
	bodyBytes, err := common.ProtoSignableBytes(envelope.Body)
	if err != nil {
		return fmt.Errorf("%w: marshal body for rm signature: %v", ErrRewardPoolOwnerSignatureInvalid, err)
	}
	if err := verifyRewardPoolOwnerSignature(msg.RewardsManagerPubkey, bodyBytes, envelope.RmOwnerSignature); err != nil {
		return err
	}
	canonical := rewards.CanonicalAuthorities(msg.Authorities)
	if !slices.Contains(canonical, strings.ToLower(strings.TrimSpace(signer))) {
		return fmt.Errorf("%w: signer %s not in initial authorities", ErrRewardUnauthorized, signer)
	}
	return insertRewardPoolIfAbsent(ctx, s.getDb(), msg.RewardsManagerPubkey, msg.Authorities)
}

// finalizeSetRewardPoolAuthorities re-checks signer authorization against
// post-prior-tx state via s.getDb(), guarding against same-block ordering
// where an earlier tx rotates the signer out before this one runs.
//
// Also re-runs the same shape validation as the live validate path
// (defense-in-depth, matching finalizeCreateRewardPool): block-sync
// replay calls FinalizeBlock without re-running ProcessProposal, so any
// malformed bytes that ever reached the chain would skip the validate-
// time checks. Repeating them here keeps the post-replay state identical
// to live and prevents an orphaned/empty-authority pool from being
// written.
func (s *Server) finalizeSetRewardPoolAuthorities(ctx context.Context, msg *corev1.SetRewardPoolAuthorities, signer string) error {
	if err := validateRewardsManagerPubkeyShape(msg.RewardsManagerPubkey); err != nil {
		return err
	}
	if err := validateAuthorityList(msg.Authorities); err != nil {
		return err
	}
	if err := s.checkPoolAuthorization(ctx, s.getDb(), msg.RewardsManagerPubkey, signer); err != nil {
		return err
	}
	return s.getDb().UpdateRewardPoolAuthorities(ctx, db.UpdateRewardPoolAuthoritiesParams{
		RewardsManagerPubkey: msg.RewardsManagerPubkey,
		Authorities:          rewards.CanonicalAuthorities(msg.Authorities),
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
	if !slices.Contains(pool.Authorities, strings.ToLower(strings.TrimSpace(signer))) {
		return fmt.Errorf("%w: signer %s not authorized for pool %s", ErrRewardUnauthorized, signer, rewardsManagerPubkey)
	}
	return nil
}
