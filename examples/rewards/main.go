package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"log"
	"os"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/rewards"
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
	if err := oap.Init(context.Background()); err != nil {
		log.Fatalf("Failed to init SDK: %v", err)
	}

	resp, err := oap.Core.GetStatus(context.Background(), connect.NewRequest(&v1.GetStatusRequest{}))
	if err != nil {
		log.Fatalf("Failed to get status: %v", err)
	}

	currentHeight := resp.Msg.ChainInfo.CurrentHeight
	deadline := currentHeight + 100

	// CreateRewardPool requires possession of the RM's ed25519 keypair —
	// the same keypair that signed the InitRewardManager instruction on
	// Solana. The launchpad relay derives this from
	// (launchpadDeterministicSecret, mint) and so always has it.
	//
	// REWARDS_MANAGER_SECRET_HEX is the 64-byte ed25519 secret key, hex-
	// encoded. We derive the public key (which IS the rewards_manager_pubkey,
	// base58-encoded) from it.
	secretHex := os.Getenv("REWARDS_MANAGER_SECRET_HEX")
	if secretHex == "" {
		log.Fatalf("REWARDS_MANAGER_SECRET_HEX environment variable is not set (64-byte ed25519 secret, hex-encoded)")
	}
	rmSecret, err := hex.DecodeString(secretHex)
	if err != nil {
		log.Fatalf("Failed to decode REWARDS_MANAGER_SECRET_HEX: %v", err)
	}
	if len(rmSecret) != ed25519.PrivateKeySize {
		log.Fatalf("REWARDS_MANAGER_SECRET_HEX must decode to %d bytes; got %d", ed25519.PrivateKeySize, len(rmSecret))
	}
	rmPrivKey := ed25519.PrivateKey(rmSecret)
	rewardsManagerPubkey := base58.Encode(rmPrivKey.Public().(ed25519.PublicKey))

	// First-class CreateReward requires an existing pool keyed by the
	// reward manager pubkey. Create one — fail loudly on any error so the
	// next call doesn't proceed against a broken setup. If the pool was
	// created in a previous run, this will surface as a "pool already
	// exists" error and the example needs to be rerun against a fresh RM
	// pubkey (or the existing pool's tx hash recorded for the reuse path).
	authorities := []string{oap.Address()}
	if _, err := oap.Rewards.CreateRewardPool(context.Background(), &v1.CreateRewardPool{
		RewardsManagerPubkey: rewardsManagerPubkey,
		Authorities:          authorities,
		RmOwnerSignature:     rewards.SignCreateRewardPool(rmPrivKey, oap.ChainID(), rewardsManagerPubkey, authorities),
	}, deadline); err != nil {
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
