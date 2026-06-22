package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log"
	"os"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/mr-tron/base58/base58"
)

func main() {
	privateKeyStr := os.Getenv("PRIVATE_KEY")
	if privateKeyStr == "" {
		log.Fatalf("PRIVATE_KEY environment variable is not set")
	}
	privateKey, err := common.EthToEthKey(privateKeyStr)
	if err != nil {
		log.Fatalf("Failed to convert private key: %v", err)
	}

	recipient := os.Getenv("RECIPIENT")
	if recipient == "" {
		log.Fatalf("RECIPIENT environment variable is not set")
	}

	oap := sdk.NewOpenAudioSDK("creatornode11.staging.audius.co")
	oap.SetPrivKey(privateKey)

	resp, err := oap.Core.GetStatus(context.Background(), connect.NewRequest(&v1.GetStatusRequest{}))
	if err != nil {
		log.Fatalf("Failed to get status: %v", err)
	}

	currentHeight := resp.Msg.ChainInfo.CurrentHeight
	deadline := currentHeight + 100

	rmPubKey, rmPrivKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatalf("Failed to generate RM keypair: %v", err)
	}
	rewardsManagerPubkey := base58.Encode(rmPubKey)

	if _, err := oap.Rewards.CreateRewardPool(context.Background(), &v1.CreateRewardPool{
		RewardsManagerPubkey: rewardsManagerPubkey,
		Authorities:          []string{oap.Address()},
	}, rmPrivKey, deadline); err != nil {
		log.Fatalf("Failed to create reward pool: %v", err)
	}

	reward, err := oap.Rewards.CreateReward(context.Background(), &v1.CreateReward{
		RewardId:             "reward1",
		Name:                 "Test Reward 1",
		Amount:               1000,
		RewardsManagerPubkey: rewardsManagerPubkey,
	}, deadline)
	if err != nil {
		log.Fatalf("Failed to create reward: %v", err)
	}
	fmt.Println("reward created at address: ", reward.Address)

	reward, err = oap.Rewards.GetReward(context.Background(), reward.Address)
	if err != nil {
		log.Fatalf("Failed to get reward: %v", err)
	}
	fmt.Println("reward id: ", reward.RewardId)

	attestation, err := oap.Rewards.GetRewardAttestation(context.Background(), &v1.GetRewardAttestationRequest{
		EthRecipientAddress: recipient,
		Amount:              1000,
		RewardAddress:       reward.Address,
		RewardId:            "reward1",
		Specifier:           "test_specifier",
		ClaimAuthority:      oap.Address(),
	})

	if err != nil {
		log.Fatalf("Failed to get reward attestation: %v", err)
	}
	fmt.Println("reward attestation: ", attestation.Attestation)
}
