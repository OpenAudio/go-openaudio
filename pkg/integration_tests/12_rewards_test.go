package integration_tests

import (
	"context"
	"strings"
	"testing"

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

	t.Run("Artist-coin attestations are rejected", func(t *testing.T) {
		authorityKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("Failed to generate authority key: %v", err)
		}
		authorityAddr := common.PrivKeyToAddress(authorityKey)
		authority := sdk.NewOpenAudioSDK(nodeUrl)
		authority.SetPrivKey(authorityKey)

		reward, err := authority.Rewards.CreateReward(ctx, &v1.CreateReward{
			RewardId: "blocked_attestation_reward",
			Name:     "Blocked Attestation Reward",
			Amount:   5000,
			ClaimAuthorities: []*v1.ClaimAuthority{
				{Address: authorityAddr, Name: "Authority"},
			},
			DeadlineBlockHeight: 999999,
		})
		if err != nil {
			t.Fatalf("Failed to create reward: %v", err)
		}

		_, err = authority.Rewards.GetRewardAttestation(ctx, &v1.GetRewardAttestationRequest{
			EthRecipientAddress: "0x1234567890123456789012345678901234567890",
			Amount:              5000,
			RewardAddress:       reward.Address,
			RewardId:            "blocked_attestation_reward",
			Specifier:           "test_specifier",
			ClaimAuthority:      authorityAddr,
			AmountDecimals:      8,
		})
		if err == nil {
			t.Fatalf("expected GetRewardAttestation to be rejected, got success")
		}
		if !strings.Contains(err.Error(), "artist-coin attestations are disabled") {
			t.Fatalf("expected disabled-attestation error, got: %v", err)
		}
	})
}
