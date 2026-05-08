package integration_tests

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/integration_tests/utils"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestRewardsLifecycle(t *testing.T) {
	ctx := context.Background()
	nodeUrl := utils.DiscoveryOneRPC

	// Wait for devnet to be ready
	if err := utils.WaitForDevnetHealthy(); err != nil {
		t.Fatalf("Devnet not ready: %v", err)
	}

	t.Run("Create, Delete, and Query Rewards", func(t *testing.T) {
		// Generate random private keys for claim authorities
		creatorKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate creator key: %v", err)
		}
		creatorAddr := common.PrivKeyToAddress(creatorKey)
		creator := sdk.NewOpenAudioSDK(nodeUrl)
		creator.SetPrivKey(creatorKey)

		deleterKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate deleter key: %v", err)
		}
		deleterAddr := common.PrivKeyToAddress(deleterKey)
		deleter := sdk.NewOpenAudioSDK(nodeUrl)
		deleter.SetPrivKey(deleterKey)

		t.Logf("creator key: %s", creatorAddr)
		t.Logf("deleter key: %s", deleterAddr)

		// Under the pool-based model authorities live on the pool, not on
		// the reward. To preserve the per-reward authority granularity of
		// the original test (reward1: creator only; reward2: creator +
		// deleter), we create two pools.
		rmPubkeyA := freshSolanaPubkey(t)
		rmPubkeyB := freshSolanaPubkey(t)
		if _, err := creator.Rewards.CreateRewardPool(ctx, &v1.CreateRewardPool{
			RewardsManagerPubkey: rmPubkeyA,
			Authorities:          []string{creatorAddr},
		}, 999999); err != nil {
			t.Fatalf("Failed to create pool A: %v", err)
		}
		if _, err := creator.Rewards.CreateRewardPool(ctx, &v1.CreateRewardPool{
			RewardsManagerPubkey: rmPubkeyB,
			Authorities:          []string{creatorAddr, deleterAddr},
		}, 999999); err != nil {
			t.Fatalf("Failed to create pool B: %v", err)
		}

		// Step 1: Create two rewards in different pools.
		reward1, err := creator.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId:             "reward1",
			Name:                 "Test Reward 1",
			Amount:               1000,
			RewardsManagerPubkey: rmPubkeyA,
		}, 999999)
		if err != nil {
			t.Fatalf("Failed to create reward1: %v", err)
		}
		t.Logf("Created reward1 at address: %s", reward1.Address)

		// Reward 2: creator and deleter as claim authorities (via pool B).
		reward2, err := creator.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId:             "reward2",
			Name:                 "Test Reward 2",
			Amount:               2000,
			RewardsManagerPubkey: rmPubkeyB,
		}, 999999)
		if err != nil {
			t.Fatalf("Failed to create reward2: %v", err)
		}
		t.Logf("Created reward2 at address: %s", reward2.Address)

		// Step 2: Query GetRewards for each user and verify correct rewards show up
		// Creator should see both rewards
		creatorRewards, err := creator.Rewards.GetRewards(ctx, creatorAddr)
		if err != nil {
			t.Fatalf("Failed to get creator rewards: %v", err)
		}
		if len(creatorRewards.Rewards) != 2 {
			t.Fatalf("Expected creator to have 2 rewards, got %d", len(creatorRewards.Rewards))
		}
		t.Logf("Creator has %d rewards", len(creatorRewards.Rewards))

		// Deleter should see only reward2
		deleterRewards, err := deleter.Rewards.GetRewards(ctx, deleterAddr)
		if err != nil {
			t.Fatalf("Failed to get deleter rewards: %v", err)
		}
		if len(deleterRewards.Rewards) != 1 {
			t.Fatalf("Expected deleter to have 1 reward, got %d", len(deleterRewards.Rewards))
		}
		if deleterRewards.Rewards[0].Address != reward2.Address {
			t.Fatalf("Expected deleter to have reward2, got different reward")
		}
		t.Logf("Deleter has %d rewards", len(deleterRewards.Rewards))

		// Step 3: Deleter deletes reward2
		deleteHash, err := deleter.Rewards.DeleteReward(ctx, &v1.DeleteReward{
			Address:             reward2.Address,
		}, 999999)
		if err != nil {
			t.Fatalf("Failed to delete reward2: %v", err)
		}
		t.Logf("Deleter successfully deleted reward2: %s", deleteHash)

		// Step 4: Verify reward2 no longer shows up in relevant GetRewards queries
		// Creator should now see only 1 reward (reward1)
		creatorRewardsAfterDelete, err := creator.Rewards.GetRewards(ctx, creatorAddr)
		if err != nil {
			t.Fatalf("Failed to get creator rewards after delete: %v", err)
		}
		if len(creatorRewardsAfterDelete.Rewards) != 1 {
			t.Fatalf("Expected creator to have 1 reward after delete, got %d", len(creatorRewardsAfterDelete.Rewards))
		}
		if creatorRewardsAfterDelete.Rewards[0].Address != reward1.Address {
			t.Fatalf("Expected creator to have only reward1 after delete")
		}
		t.Logf("Creator has %d rewards after delete", len(creatorRewardsAfterDelete.Rewards))

		// Deleter should now see 0 rewards
		deleterRewardsAfterDelete, err := deleter.Rewards.GetRewards(ctx, deleterAddr)
		if err != nil {
			t.Fatalf("Failed to get deleter rewards after delete: %v", err)
		}
		if len(deleterRewardsAfterDelete.Rewards) != 0 {
			t.Fatalf("Expected deleter to have 0 rewards after delete, got %d", len(deleterRewardsAfterDelete.Rewards))
		}
		t.Logf("Deleter has %d rewards after delete", len(deleterRewardsAfterDelete.Rewards))

		t.Logf("All reward lifecycle tests passed successfully!")
	})

	t.Run("Test Reward Attestations with Claim Authorities", func(t *testing.T) {

		// Generate random private keys for claim authorities
		authority1Key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate authority1 key: %v", err)
		}
		authority1Addr := common.PrivKeyToAddress(authority1Key)
		authority1 := sdk.NewOpenAudioSDK(nodeUrl)
		authority1.SetPrivKey(authority1Key)

		authority2Key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate authority2 key: %v", err)
		}
		authority2Addr := common.PrivKeyToAddress(authority2Key)
		authority2 := sdk.NewOpenAudioSDK(nodeUrl)
		authority2.SetPrivKey(authority2Key)

		unauthorizedKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate unauthorized key: %v", err)
		}
		unauthorizedAddr := common.PrivKeyToAddress(unauthorizedKey)
		unauthorized := sdk.NewOpenAudioSDK(nodeUrl)
		unauthorized.SetPrivKey(unauthorizedKey)

		t.Logf("authority1 address: %s", authority1Addr)
		t.Logf("authority2 address: %s", authority2Addr)
		t.Logf("unauthorized address: %s", unauthorizedAddr)

		// Create a pool with both authorities and a reward in it.
		rmPubkey := freshSolanaPubkey(t)
		if _, err := authority1.Rewards.CreateRewardPool(ctx, &v1.CreateRewardPool{
			RewardsManagerPubkey: rmPubkey,
			Authorities:          []string{authority1Addr, authority2Addr},
		}, 999999); err != nil {
			t.Fatalf("Failed to create pool: %v", err)
		}
		reward, err := authority1.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId:             "attestation_test_reward",
			Name:                 "Attestation Test Reward",
			Amount:               5000,
			RewardsManagerPubkey: rmPubkey,
		}, 999999)
		if err != nil {
			t.Fatalf("Failed to create reward: %v", err)
		}
		t.Logf("Created reward at address: %s", reward.Address)

		// Test recipient address
		recipientAddr := "0x1234567890123456789012345678901234567890"
		specifier := "test_specifier_123"

		// Test 1: authority1 should be able to get attestation
		attestation1, err := authority1.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: recipientAddr,
			Amount:              5000,
			RewardAddress:       reward.Address,
			RewardId:            "attestation_test_reward",
			Specifier:           specifier,
			ClaimAuthority:      authority1Addr,
			AmountDecimals:      8,
		})
		if err != nil {
			t.Fatalf("authority1 should be able to get attestation: %v", err)
		}
		t.Logf("authority1 successfully got attestation: %s", attestation1.Attestation)

		// Test 2: authority2 should be able to get attestation
		attestation2, err := authority2.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: recipientAddr,
			Amount:              5000,
			RewardAddress:       reward.Address,
			RewardId:            "attestation_test_reward",
			Specifier:           specifier,
			ClaimAuthority:      authority2Addr,
			AmountDecimals:      8,
		})
		if err != nil {
			t.Fatalf("authority2 should be able to get attestation: %v", err)
		}
		t.Logf("authority2 successfully got attestation: %s", attestation2.Attestation)

		// Test 3: unauthorized user should NOT be able to get attestation.
		// Amount must match the reward amount (5000) so that the call
		// passes Validate and actually exercises the auth gate; otherwise
		// it would fail at amount validation instead.
		_, err = unauthorized.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: recipientAddr,
			Amount:              5000,
			RewardAddress:       reward.Address,
			RewardId:            "attestation_test_reward",
			Specifier:           specifier,
			ClaimAuthority:      unauthorizedAddr,
			AmountDecimals:      8,
		})
		if err == nil {
			t.Fatalf("unauthorized user should NOT be able to get attestation, but it succeeded")
		}
		if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
			t.Fatalf("expected PermissionDenied for unauthorized user, got %v: %v", got, err)
		}
		t.Logf("unauthorized user correctly failed to get attestation: %v", err)

		// Test 4: Verify authority1 cannot get attestation for a reward they're not authorized for
		// Create another pool with only authority2 + a reward in it.
		rmPubkey2 := freshSolanaPubkey(t)
		if _, err := authority2.Rewards.CreateRewardPool(ctx, &v1.CreateRewardPool{
			RewardsManagerPubkey: rmPubkey2,
			Authorities:          []string{authority2Addr},
		}, 999999); err != nil {
			t.Fatalf("Failed to create pool 2: %v", err)
		}
		reward2, err := authority2.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId:             "attestation_test_reward_2",
			Name:                 "Attestation Test Reward 2",
			Amount:               3000,
			RewardsManagerPubkey: rmPubkey2,
		}, 999999)
		if err != nil {
			t.Fatalf("Failed to create reward2: %v", err)
		}
		t.Logf("Created reward2 at address: %s", reward2.Address)

		// authority1 should NOT be able to get attestation for reward2.
		// Amount matches reward2 (3000) so the call reaches the auth gate
		// rather than failing earlier at amount validation.
		_, err = authority1.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: recipientAddr,
			Amount:              3000,
			RewardAddress:       reward2.Address,
			RewardId:            "attestation_test_reward_2",
			Specifier:           specifier,
			ClaimAuthority:      authority1Addr,
			AmountDecimals:      8,
		})
		if err == nil {
			t.Fatalf("authority1 should NOT be able to get attestation for reward2, but it succeeded")
		}
		if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
			t.Fatalf("expected PermissionDenied for cross-pool authority, got %v: %v", got, err)
		}
		t.Logf("authority1 correctly failed to get attestation for reward2: %v", err)

		t.Logf("All reward attestation tests passed successfully!")
	})

	t.Run("Test with Amount Validation", func(t *testing.T) {

		// Generate a new claim authority key
		authorityKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate authority key: %v", err)
		}
		authorityAddr := common.PrivKeyToAddress(authorityKey)
		authority := sdk.NewOpenAudioSDK(nodeUrl)
		authority.SetPrivKey(authorityKey)

		// Generate a key for creating the reward
		creatorKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate creator key: %v", err)
		}
		creator := sdk.NewOpenAudioSDK(nodeUrl)
		creator.SetPrivKey(creatorKey)

		// Create a pool that names authority + a reward bound to it.
		rmPubkey := freshSolanaPubkey(t)
		if _, err := creator.Rewards.CreateRewardPool(ctx, &v1.CreateRewardPool{
			RewardsManagerPubkey: rmPubkey,
			Authorities:          []string{authorityAddr},
		}, 999999); err != nil {
			t.Fatalf("Failed to create pool: %v", err)
		}
		reward, err := creator.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId:             "amount_test",
			Name:                 "Amount Test Reward",
			Amount:               100,
			RewardsManagerPubkey: rmPubkey,
		}, 999999)
		if err != nil {
			t.Fatalf("Failed to create reward: %v", err)
		}
		t.Logf("Created reward at address: %s", reward.Address)

		// Test recipient address
		recipientAddr := "0xe811761771ef65f9de0b64d6335f3b8ff50adc44"
		specifier := "test_specifier_amount"

		// Test 1: Correct amount should succeed
		attestation, err := authority.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: recipientAddr,
			Amount:              100, // Matches reward amount
			RewardAddress:       reward.Address,
			RewardId:            "amount_test",
			Specifier:           specifier,
			ClaimAuthority:      authorityAddr,
			AmountDecimals:      8,
		})
		if err != nil {
			t.Fatalf("Should succeed with correct amount: %v", err)
		}
		t.Logf("Successfully got attestation with correct amount: %s", attestation.Attestation)

		// Test 2: Wrong amount should fail
		_, err = authority.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: recipientAddr,
			Amount:              50, // Wrong amount
			RewardAddress:       reward.Address,
			RewardId:            "amount_test",
			Specifier:           specifier,
			ClaimAuthority:      authorityAddr,
			AmountDecimals:      8,
		})
		if err == nil {
			t.Fatalf("Should have failed with wrong amount")
		}
		t.Logf("Correctly failed with wrong amount: %v", err)

		// Test 3: Zero amount should fail
		_, err = authority.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: recipientAddr,
			Amount:              0, // Zero amount
			RewardAddress:       reward.Address,
			RewardId:            "amount_test",
			Specifier:           specifier,
			ClaimAuthority:      authorityAddr,
			AmountDecimals:      8,
		})
		if err == nil {
			t.Fatalf("Should have failed with zero amount")
		}
		t.Logf("Correctly failed with zero amount: %v", err)

		// Test 4: Invalid decimals should fail
		_, err = authority.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: recipientAddr,
			Amount:              100,
			RewardAddress:       reward.Address,
			RewardId:            "amount_test",
			Specifier:           specifier,
			ClaimAuthority:      authorityAddr,
			AmountDecimals:      19,
		})
		if err == nil {
			t.Fatalf("Should have failed with invalid amount_decimals")
		}
		t.Logf("Correctly failed with invalid amount_decimals: %v", err)
		if err == nil {
			t.Fatalf("Should have failed with zero amount")
		}
		t.Logf("Correctly failed with zero amount: %v", err)

		t.Logf("Amount validation test passed successfully!")
	})
}
