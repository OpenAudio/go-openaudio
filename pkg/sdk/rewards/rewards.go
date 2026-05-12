package rewards

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"errors"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	pkgrewards "github.com/OpenAudio/go-openaudio/pkg/rewards"
)

type Rewards struct {
	privKey *ecdsa.PrivateKey
	core    corev1connect.CoreServiceClient
}

func NewRewards(core corev1connect.CoreServiceClient) *Rewards {
	return &Rewards{
		core: core,
	}
}

func (r *Rewards) SetPrivKey(privKey *ecdsa.PrivateKey) {
	r.privKey = privKey
}

// signAndSendReward signs the body, wraps it in the RewardMessage envelope
// alongside the signature, and submits the resulting SignedTransaction.
func (r *Rewards) signAndSendReward(ctx context.Context, body *v1.RewardBody) (string, error) {
	sig, err := common.ProtoSign(r.privKey, body)
	if err != nil {
		return "", err
	}
	tx := &v1.SendTransactionRequest{
		Transaction: &v1.SignedTransaction{
			Transaction: &v1.SignedTransaction_Reward{
				Reward: &v1.RewardMessage{Body: body, Signature: sig},
			},
		},
	}
	resp, err := r.core.SendTransaction(ctx, connect.NewRequest(tx))
	if err != nil {
		return "", err
	}
	return resp.Msg.GetTransaction().GetHash(), nil
}

// signAndSendRewardPool signs the body with the envelope signer's
// secp256k1 key and submits. rmOwnerSig, when non-nil, is placed in the
// envelope's rm_owner_signature field. It is required when the body's
// action is CreateRewardPool (the validator rejects unsigned creates as
// frontrunning) and ignored otherwise. Callers produce the ed25519
// signature over common.ProtoSignableBytes(body) using the RM keypair —
// see CreateRewardPool below for the canonical helper.
func (r *Rewards) signAndSendRewardPool(ctx context.Context, body *v1.RewardPoolBody, rmOwnerSig []byte) (string, error) {
	sig, err := common.ProtoSign(r.privKey, body)
	if err != nil {
		return "", err
	}
	tx := &v1.SendTransactionRequest{
		Transaction: &v1.SignedTransaction{
			Transaction: &v1.SignedTransaction_RewardPool{
				RewardPool: &v1.RewardPoolMessage{
					Body:             body,
					Signature:        sig,
					RmOwnerSignature: rmOwnerSig,
				},
			},
		},
	}
	resp, err := r.core.SendTransaction(ctx, connect.NewRequest(tx))
	if err != nil {
		return "", err
	}
	return resp.Msg.GetTransaction().GetHash(), nil
}

func (r *Rewards) CreateReward(ctx context.Context, cr *v1.CreateReward, deadlineBlockHeight int64) (*v1.GetRewardResponse, error) {
	body := &v1.RewardBody{
		DeadlineBlockHeight: deadlineBlockHeight,
		Action:              &v1.RewardBody_Create{Create: cr},
	}
	txhash, err := r.signAndSendReward(ctx, body)
	if err != nil {
		return nil, err
	}
	reward, err := r.core.GetReward(ctx, connect.NewRequest(&v1.GetRewardRequest{Txhash: txhash}))
	if err != nil {
		return nil, err
	}
	return reward.Msg, nil
}

func (r *Rewards) DeleteReward(ctx context.Context, dr *v1.DeleteReward, deadlineBlockHeight int64) (string, error) {
	body := &v1.RewardBody{
		DeadlineBlockHeight: deadlineBlockHeight,
		Action:              &v1.RewardBody_Delete{Delete: dr},
	}
	return r.signAndSendReward(ctx, body)
}

func (r *Rewards) GetReward(ctx context.Context, address string) (*v1.GetRewardResponse, error) {
	req := connect.NewRequest(&v1.GetRewardRequest{
		Address: address,
	})
	resp, err := r.core.GetReward(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (r *Rewards) GetRewards(ctx context.Context, claim_authority string) (*v1.GetRewardsResponse, error) {
	if claim_authority == "" {
		return nil, errors.New("claim_authority required")
	}
	req := connect.NewRequest(&v1.GetRewardsRequest{
		ClaimAuthority: claim_authority,
	})
	resp, err := r.core.GetRewards(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CreateRewardPool submits a CreateRewardPool transaction. The pool is
// keyed by msg.RewardsManagerPubkey (the Solana reward manager pubkey it
// will govern, base58 32 bytes); subsequent SetRewardPoolAuthorities /
// CreateReward calls reference the pool by this same value.
//
// rmKey is the ed25519 PRIVATE key whose public key is
// msg.RewardsManagerPubkey — i.e., the Solana RM state account's
// keypair. For launchpad-minted coins, the relay derives this from
// (launchpadDeterministicSecret, mint). The validator requires the
// envelope-level rm_owner_signature to verify against
// msg.RewardsManagerPubkey, so possession of the matching ed25519 secret
// is the proof-of-RM-control that defeats pool-creation frontrunning.
//
// Returns the cometbft tx hash.
func (r *Rewards) CreateRewardPool(ctx context.Context, msg *v1.CreateRewardPool, rmKey ed25519.PrivateKey, deadlineBlockHeight int64) (string, error) {
	body := &v1.RewardPoolBody{
		DeadlineBlockHeight: deadlineBlockHeight,
		Action:              &v1.RewardPoolBody_Create{Create: msg},
	}
	bodyBytes, err := common.ProtoSignableBytes(body)
	if err != nil {
		return "", err
	}
	rmSig := ed25519.Sign(rmKey, bodyBytes)
	return r.signAndSendRewardPool(ctx, body, rmSig)
}

// GetRewardPool fetches a pool by its rewards_manager_pubkey (Solana RM
// pubkey, base58, 32 bytes).
func (r *Rewards) GetRewardPool(ctx context.Context, rewardsManagerPubkey string) (*v1.GetRewardPoolResponse, error) {
	resp, err := r.core.GetRewardPool(ctx, connect.NewRequest(&v1.GetRewardPoolRequest{RewardsManagerPubkey: rewardsManagerPubkey}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// SetRewardPoolAuthorities replaces the pool's authority set wholesale. The
// caller composes the desired list (current minus the one to remove, current
// plus the one to add, etc.); add and remove are derived views. No RM
// keypair signature is needed — rotation is gated by signer ∈ current
// pool authorities, by design (the launchpad needn't be online to rotate).
func (r *Rewards) SetRewardPoolAuthorities(ctx context.Context, msg *v1.SetRewardPoolAuthorities, deadlineBlockHeight int64) (string, error) {
	body := &v1.RewardPoolBody{
		DeadlineBlockHeight: deadlineBlockHeight,
		Action:              &v1.RewardPoolBody_SetAuthorities{SetAuthorities: msg},
	}
	return r.signAndSendRewardPool(ctx, body, nil)
}

// GetRewardSenderAttestation requests an attestation that the validator will
// sign authorizing addr to be added as a sender on the Solana reward manager
// account identified by rewardsManagerPubkey. For pool-managed RMs, the
// validator signs iff addr ∈ pool.authorities; for unmanaged RMs (notably
// AUDIO), the validator falls back to its validator/AAO trust set. The
// returned attestation is meant to be combined with attestations from other
// validators and submitted to Solana via CreateSenderPublic.
func (r *Rewards) GetRewardSenderAttestation(ctx context.Context, addr string, rewardsManagerPubkey string) (*v1.GetRewardSenderAttestationResponse, error) {
	resp, err := r.core.GetRewardSenderAttestation(ctx, connect.NewRequest(&v1.GetRewardSenderAttestationRequest{
		Address:              addr,
		RewardsManagerPubkey: rewardsManagerPubkey,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// GetDeleteRewardSenderAttestation is the inverse of GetRewardSenderAttestation:
// the validator signs an attestation authorizing the removal of addr as a
// sender on the Solana RM. For pool-managed RMs, the validator signs iff
// addr ∉ pool.authorities (proving OAP-side rotation already happened).
// Used to deregister leaked / rotated-out keys from Solana.
func (r *Rewards) GetDeleteRewardSenderAttestation(ctx context.Context, addr string, rewardsManagerPubkey string) (*v1.GetDeleteRewardSenderAttestationResponse, error) {
	resp, err := r.core.GetDeleteRewardSenderAttestation(ctx, connect.NewRequest(&v1.GetDeleteRewardSenderAttestationRequest{
		Address:              addr,
		RewardsManagerPubkey: rewardsManagerPubkey,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (r *Rewards) GetRewardAttestation(ctx context.Context, req *v1.GetRewardAttestationRequest) (*v1.GetRewardAttestationResponse, error) {
	// Create a RewardClaim to compile the data in the correct format
	claim := pkgrewards.RewardClaim{
		RecipientEthAddress: req.EthRecipientAddress,
		Amount:              req.Amount,
		RewardID:            req.RewardId,
		Specifier:           req.Specifier,
		ClaimAuthority:      req.ClaimAuthority, // Use claim authority as oracle
		Decimals:            req.AmountDecimals,
	}

	// Use the utility function to sign the claim
	signature, err := pkgrewards.SignClaim(claim, r.privKey)
	if err != nil {
		return nil, err
	}

	req.Signature = signature

	connectReq := connect.NewRequest(req)
	resp, err := r.core.GetRewardAttestation(ctx, connectReq)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}
