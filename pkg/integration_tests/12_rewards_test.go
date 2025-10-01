package integration_tests

import (
	"context"
	"testing"
	"time"

	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/integration_tests/utils"
	"github.com/AudiusProject/audiusd/pkg/sdk"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestRewardsLifecycle(t *testing.T) {
	ctx := context.Background()
	nodeUrl := utils.DiscoveryOneRPC

	// Wait for devnet to be ready
	if err := utils.WaitForDevnetHealthy(30 * time.Second); err != nil {
		t.Fatalf("Devnet not ready: %v", err)
	}

	t.Run("Create, Delete, and Query Rewards", func(t *testing.T) {
		// Generate random private keys for claim authorities
		creatorKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate creator key: %v", err)
		}
		creatorAddr := common.PrivKeyToAddress(creatorKey)
		creator := sdk.NewAudiusdSDK(nodeUrl)
		creator.SetPrivKey(creatorKey)

		deleterKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate deleter key: %v", err)
		}
		deleterAddr := common.PrivKeyToAddress(deleterKey)
		deleter := sdk.NewAudiusdSDK(nodeUrl)
		deleter.SetPrivKey(deleterKey)

		t.Logf("creator key: %s", creatorAddr)
		t.Logf("deleter key: %s", deleterAddr)

		// Step 1: Create two rewards with different claim authorities
		// Reward 1: only creator as claim authority
		reward1, err := creator.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId: "reward1",
			Name:     "Test Reward 1",
			Amount:   1000,
			ClaimAuthorities: []*v1.ClaimAuthority{
				{Address: creatorAddr, Name: "Creator"},
			},
			DeadlineBlockHeight: 999999,
		})
		if err != nil {
			t.Fatalf("Failed to create reward1: %v", err)
		}
		t.Logf("Created reward1 at address: %s", reward1.Address)

		// Reward 2: creator and deleter as claim authorities
		reward2, err := creator.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId: "reward2",
			Name:     "Test Reward 2",
			Amount:   2000,
			ClaimAuthorities: []*v1.ClaimAuthority{
				{Address: creatorAddr, Name: "Creator"},
				{Address: deleterAddr, Name: "Deleter"},
			},
			DeadlineBlockHeight: 999999,
		})
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
			DeadlineBlockHeight: 999999,
		})
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
		authority1 := sdk.NewAudiusdSDK(nodeUrl)
		authority1.SetPrivKey(authority1Key)

		authority2Key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate authority2 key: %v", err)
		}
		authority2Addr := common.PrivKeyToAddress(authority2Key)
		authority2 := sdk.NewAudiusdSDK(nodeUrl)
		authority2.SetPrivKey(authority2Key)

		unauthorizedKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate unauthorized key: %v", err)
		}
		unauthorizedAddr := common.PrivKeyToAddress(unauthorizedKey)
		unauthorized := sdk.NewAudiusdSDK(nodeUrl)
		unauthorized.SetPrivKey(unauthorizedKey)

		t.Logf("authority1 address: %s", authority1Addr)
		t.Logf("authority2 address: %s", authority2Addr)
		t.Logf("unauthorized address: %s", unauthorizedAddr)

		// Create a reward with authority1 and authority2 as claim authorities
		reward, err := authority1.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId: "attestation_test_reward",
			Name:     "Attestation Test Reward",
			Amount:   5000,
			ClaimAuthorities: []*v1.ClaimAuthority{
				{Address: authority1Addr, Name: "Authority 1"},
				{Address: authority2Addr, Name: "Authority 2"},
			},
			DeadlineBlockHeight: 999999,
		})
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
			Amount:              1000,
			RewardAddress:       reward.Address,
			RewardId:            "attestation_test_reward",
			Specifier:           specifier,
			ClaimAuthority:      authority1Addr,
		})
		if err != nil {
			t.Fatalf("authority1 should be able to get attestation: %v", err)
		}
		t.Logf("authority1 successfully got attestation: %s", attestation1.Attestation)

		// Test 2: authority2 should be able to get attestation
		attestation2, err := authority2.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: recipientAddr,
			Amount:              1000,
			RewardAddress:       reward.Address,
			RewardId:            "attestation_test_reward",
			Specifier:           specifier,
			ClaimAuthority:      authority2Addr,
		})
		if err != nil {
			t.Fatalf("authority2 should be able to get attestation: %v", err)
		}
		t.Logf("authority2 successfully got attestation: %s", attestation2.Attestation)

		// Test 3: unauthorized user should NOT be able to get attestation
		_, err = unauthorized.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: recipientAddr,
			Amount:              1000,
			RewardAddress:       reward.Address,
			RewardId:            "attestation_test_reward",
			Specifier:           specifier,
			ClaimAuthority:      unauthorizedAddr,
		})
		if err == nil {
			t.Fatalf("unauthorized user should NOT be able to get attestation, but it succeeded")
		}
		t.Logf("unauthorized user correctly failed to get attestation: %v", err)

		// Test 4: Verify authority1 cannot get attestation for a reward they're not authorized for
		// Create another reward with only authority2
		reward2, err := authority2.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId: "attestation_test_reward_2",
			Name:     "Attestation Test Reward 2",
			Amount:   3000,
			ClaimAuthorities: []*v1.ClaimAuthority{
				{Address: authority2Addr, Name: "Authority 2"},
			},
			DeadlineBlockHeight: 999999,
		})
		if err != nil {
			t.Fatalf("Failed to create reward2: %v", err)
		}
		t.Logf("Created reward2 at address: %s", reward2.Address)

		// authority1 should NOT be able to get attestation for reward2
		_, err = authority1.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: recipientAddr,
			Amount:              500,
			RewardAddress:       reward2.Address,
			RewardId:            "attestation_test_reward_2",
			Specifier:           specifier,
			ClaimAuthority:      authority1Addr,
		})
		if err == nil {
			t.Fatalf("authority1 should NOT be able to get attestation for reward2, but it succeeded")
		}
		t.Logf("authority1 correctly failed to get attestation for reward2: %v", err)

		t.Logf("All reward attestation tests passed successfully!")
	})
}
