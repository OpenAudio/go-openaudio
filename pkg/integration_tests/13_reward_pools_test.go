package integration_tests

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/integration_tests/utils"
	pkgrewards "github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/mr-tron/base58/base58"
)

// freshRewardManager returns a random ed25519 keypair representing a
// freshly-minted Solana reward manager state account. The pubkey (base58-
// encoded) doubles as the cometbft rewards_manager_pubkey; the private
// key is needed to sign CreateRewardPool.RmOwnerSignature, which proves
// to the validator that the caller controls the RM keypair (defeating
// frontrunning of pool creation).
func freshRewardManager(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return base58.Encode(pub), priv
}

// freshSolanaPubkey is a convenience wrapper for tests that only need a
// well-formed Solana pubkey shape and don't need to sign with the keypair
// (e.g., negative tests that exercise rejection on the pubkey itself).
func freshSolanaPubkey(t *testing.T) string {
	t.Helper()
	pub, _ := freshRewardManager(t)
	return pub
}

// signedCreateRewardPool builds a CreateRewardPool message with a valid
// RmOwnerSignature, given the RM keypair, the SDK's chainID (for cross-
// chain replay protection in the signed payload), and the desired initial
// authorities. Centralizes the boilerplate so test sites read at the
// "what" level, not the "how".
func signedCreateRewardPool(s *sdk.OpenAudioSDK, rmPubkey string, rmPriv ed25519.PrivateKey, authorities []string) *v1.CreateRewardPool {
	return &v1.CreateRewardPool{
		RewardsManagerPubkey: rmPubkey,
		Authorities:          authorities,
		RmOwnerSignature:     pkgrewards.SignCreateRewardPool(rmPriv, s.ChainID(), rmPubkey, authorities),
	}
}

// TestRewardPoolsLifecycle exercises the cometbft RewardPool transactions:
// pool creation, gating CreateReward on pool membership, rotating
// authorities via SetRewardPoolAuthorities, and verifying that a rotated-out
// signer is no longer accepted.
func TestRewardPoolsLifecycle(t *testing.T) {
	ctx := context.Background()
	nodeUrl := utils.DiscoveryOneRPC

	if err := utils.WaitForDevnetHealthy(); err != nil {
		t.Fatalf("Devnet not ready: %v", err)
	}

	// Each test run gets its own fresh RM keypair so reruns don't collide on
	// the pool's identity (uniqueness is enforced server-side). The private
	// key is needed to produce the RmOwnerSignature on CreateRewardPool.
	rmPubkey, rmPriv := freshRewardManager(t)
	otherRmPubkey, otherRmPriv := freshRewardManager(t)

	aliceKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("alice key: %v", err)
	}
	aliceAddr := common.PrivKeyToAddress(aliceKey)
	alice := sdk.NewOpenAudioSDK(nodeUrl)
	alice.SetPrivKey(aliceKey)
	if err := alice.Init(ctx); err != nil {
		t.Fatalf("alice init: %v", err)
	}

	bobKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("bob key: %v", err)
	}
	bobAddr := common.PrivKeyToAddress(bobKey)
	bob := sdk.NewOpenAudioSDK(nodeUrl)
	bob.SetPrivKey(bobKey)

	malloryKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("mallory key: %v", err)
	}
	mallory := sdk.NewOpenAudioSDK(nodeUrl)
	mallory.SetPrivKey(malloryKey)

	t.Run("CreateRewardPool rejects non-pubkey rewards_manager_pubkey", func(t *testing.T) {
		// Pubkey shape check fires before signature verification, so the
		// signature value here doesn't matter — we just need a non-base58
		// rewards_manager_pubkey to land at the shape rejection.
		_, err := alice.Rewards.CreateRewardPool(ctx, &v1.CreateRewardPool{
			RewardsManagerPubkey: "not-a-real-pubkey",
			Authorities:          []string{aliceAddr},
		}, 999999)
		if err == nil {
			t.Fatalf("expected non-pubkey rewards_manager_pubkey to be rejected")
		}
	})

	t.Run("CreateRewardPool rejects missing rm_owner_signature", func(t *testing.T) {
		// Valid pubkey, valid authorities, signer ∈ authorities — but no
		// signature. Must fail at the rm_owner_signature check.
		_, err := alice.Rewards.CreateRewardPool(ctx, &v1.CreateRewardPool{
			RewardsManagerPubkey: otherRmPubkey,
			Authorities:          []string{aliceAddr},
		}, 999999)
		if err == nil {
			t.Fatalf("expected missing rm_owner_signature to be rejected")
		}
	})

	t.Run("CreateRewardPool rejects signature signed by wrong RM keypair", func(t *testing.T) {
		// Signature is well-formed and the inner ed25519 verifies — but
		// against the WRONG public key (rmPriv signs but message claims
		// otherRmPubkey). Defends against attacker reusing a signature
		// they made for their own RM against a different RM.
		msg := signedCreateRewardPool(alice, otherRmPubkey, rmPriv, []string{aliceAddr})
		_, err := alice.Rewards.CreateRewardPool(ctx, msg, 999999)
		if err == nil {
			t.Fatalf("expected mismatched-keypair signature to be rejected")
		}
	})

	t.Run("CreateRewardPool requires signer in initial authorities", func(t *testing.T) {
		// Mallory submits a perfectly-signed CreateRewardPool but isn't in
		// the initial authorities. The RM-keypair gate passes; the
		// signer-membership gate fails.
		_, err := mallory.Rewards.CreateRewardPool(ctx, signedCreateRewardPool(alice, otherRmPubkey, otherRmPriv, []string{aliceAddr, bobAddr}), 999999)
		if err == nil {
			t.Fatalf("expected CreateRewardPool to reject signer not in initial authorities")
		}
		if _, err := alice.Rewards.GetRewardPool(ctx, otherRmPubkey); err == nil {
			t.Fatalf("rejected pool should not have been persisted")
		}
	})

	t.Run("Alice creates the pool with [alice, bob]", func(t *testing.T) {
		_, err := alice.Rewards.CreateRewardPool(ctx, signedCreateRewardPool(alice, rmPubkey, rmPriv, []string{aliceAddr, bobAddr}), 999999)
		if err != nil {
			t.Fatalf("create pool: %v", err)
		}

		pool, err := alice.Rewards.GetRewardPool(ctx, rmPubkey)
		if err != nil {
			t.Fatalf("get pool: %v", err)
		}
		if pool.RewardsManagerPubkey != rmPubkey {
			t.Fatalf("rewards_manager_pubkey: got %q want %q", pool.RewardsManagerPubkey, rmPubkey)
		}
		if !containsFold(pool.Authorities, aliceAddr) || !containsFold(pool.Authorities, bobAddr) {
			t.Fatalf("authorities missing alice/bob: %v", pool.Authorities)
		}
	})

	t.Run("CreateRewardPool with duplicate rewards_manager_pubkey is rejected", func(t *testing.T) {
		_, err := alice.Rewards.CreateRewardPool(ctx, signedCreateRewardPool(alice, rmPubkey, rmPriv, []string{aliceAddr}), 999999)
		if err == nil {
			t.Fatalf("expected duplicate rewards_manager_pubkey to be rejected")
		}
	})

	t.Run("Pool member can create a reward in the pool", func(t *testing.T) {
		reward, err := bob.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId:             "pool-reward-1",
			Name:                 "Pool Reward",
			Amount:               500,
			RewardsManagerPubkey: rmPubkey,
		}, 999999)
		if err != nil {
			t.Fatalf("bob create reward: %v", err)
		}
		if reward.Address == "" {
			t.Fatalf("expected reward address")
		}

		// GetRewards must accept caller's checksum-case address (which is what
		// common.PrivKeyToAddress returns) even though stored authorities are
		// lowercased by CanonicalAuthorities. Guards against the case-
		// sensitivity regression.
		listed, err := bob.Rewards.GetRewards(ctx, bobAddr)
		if err != nil {
			t.Fatalf("get rewards by checksum-case bob: %v", err)
		}
		found := false
		for _, r := range listed.Rewards {
			if r.Address == reward.Address {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected pool-attached reward %s in GetRewards(%s); got %d rewards", reward.Address, bobAddr, len(listed.Rewards))
		}
	})

	t.Run("Non-member cannot create a reward in the pool", func(t *testing.T) {
		_, err := mallory.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId:             "pool-reward-evil",
			Name:                 "Evil Reward",
			Amount:               9999,
			RewardsManagerPubkey: rmPubkey,
		}, 999999)
		if err == nil {
			t.Fatalf("expected non-member CreateReward to be rejected")
		}
	})

	t.Run("CreateReward without rewards_manager_pubkey is rejected", func(t *testing.T) {
		_, err := alice.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId: "no-rm",
			Name:     "no rm",
			Amount:   1,
		}, 999999)
		if err == nil {
			t.Fatalf("expected CreateReward without rewards_manager_pubkey to be rejected")
		}
	})

	t.Run("SetRewardPoolAuthorities cannot be called by non-member", func(t *testing.T) {
		_, err := mallory.Rewards.SetRewardPoolAuthorities(ctx, &v1.SetRewardPoolAuthorities{
			RewardsManagerPubkey: rmPubkey,
			Authorities:          []string{aliceAddr},
		}, 999999)
		if err == nil {
			t.Fatalf("expected non-member SetRewardPoolAuthorities to be rejected")
		}
	})

	t.Run("SetRewardPoolAuthorities cannot set empty list", func(t *testing.T) {
		_, err := alice.Rewards.SetRewardPoolAuthorities(ctx, &v1.SetRewardPoolAuthorities{
			RewardsManagerPubkey: rmPubkey,
			Authorities:          []string{},
		}, 999999)
		if err == nil {
			t.Fatalf("expected empty authorities to be rejected")
		}
	})

	t.Run("SetRewardPoolAuthorities rejects non-eth-address entries", func(t *testing.T) {
		_, err := alice.Rewards.SetRewardPoolAuthorities(ctx, &v1.SetRewardPoolAuthorities{
			RewardsManagerPubkey: rmPubkey,
			Authorities:          []string{"not-an-address"},
		}, 999999)
		if err == nil {
			t.Fatalf("expected non-eth-address authority to be rejected (would orphan pool)")
		}
	})

	t.Run("Rotate: alice removes bob, leaving only alice", func(t *testing.T) {
		_, err := alice.Rewards.SetRewardPoolAuthorities(ctx, &v1.SetRewardPoolAuthorities{
			RewardsManagerPubkey: rmPubkey,
			Authorities:          []string{aliceAddr},
		}, 999999)
		if err != nil {
			t.Fatalf("rotate: %v", err)
		}
		pool, err := alice.Rewards.GetRewardPool(ctx, rmPubkey)
		if err != nil {
			t.Fatalf("get pool after rotate: %v", err)
		}
		if len(pool.Authorities) != 1 || !strings.EqualFold(pool.Authorities[0], aliceAddr) {
			t.Fatalf("after rotate expected only alice; got %v", pool.Authorities)
		}
	})

	t.Run("Bob can no longer create a reward in the pool after rotation", func(t *testing.T) {
		_, err := bob.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId:             "pool-reward-stale",
			Name:                 "Stale Reward",
			Amount:               1,
			RewardsManagerPubkey: rmPubkey,
		}, 999999)
		if err == nil {
			t.Fatalf("expected rotated-out bob to be rejected")
		}
	})

	// === PR3 sender attestation flow ===
	//
	// Pool at rmPubkey now has authorities = [alice]; bob has been rotated out.
	// Validator should: sign create for alice (current authority), refuse
	// create for bob (not in pool), sign delete for bob (rotated out → ok to
	// remove from Solana), refuse delete for alice (still authorized).

	t.Run("CreateSender attestation: pool member alice is signed", func(t *testing.T) {
		resp, err := alice.Rewards.GetRewardSenderAttestation(ctx, aliceAddr, rmPubkey)
		if err != nil {
			t.Fatalf("expected create-sender attestation for current authority alice: %v", err)
		}
		if resp.Attestation == "" {
			t.Fatalf("expected attestation string")
		}
	})

	t.Run("CreateSender attestation: rotated-out bob is rejected", func(t *testing.T) {
		_, err := alice.Rewards.GetRewardSenderAttestation(ctx, bobAddr, rmPubkey)
		if err == nil {
			t.Fatalf("expected create-sender attestation to be refused for rotated-out bob")
		}
	})

	t.Run("DeleteSender attestation: rotated-out bob is signed", func(t *testing.T) {
		resp, err := alice.Rewards.GetDeleteRewardSenderAttestation(ctx, bobAddr, rmPubkey)
		if err != nil {
			t.Fatalf("expected delete-sender attestation for rotated-out bob: %v", err)
		}
		if resp.Attestation == "" {
			t.Fatalf("expected attestation string")
		}
	})

	t.Run("DeleteSender attestation: current authority alice is rejected", func(t *testing.T) {
		_, err := alice.Rewards.GetDeleteRewardSenderAttestation(ctx, aliceAddr, rmPubkey)
		if err == nil {
			t.Fatalf("expected delete-sender attestation to be refused for current authority alice")
		}
	})
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}
