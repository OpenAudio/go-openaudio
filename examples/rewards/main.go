package main

import (
	"context"
	"fmt"
	"log"
	"os"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
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

	oap := sdk.NewOpenAudioSDK("creatornode11.staging.audius.co")
	oap.SetPrivKey(privateKey)

	reward, err := oap.Rewards.CreateReward(context.Background(), &v1.CreateReward{
		RewardId: "reward1",
		Name:     "Test Reward 1",
		Amount:   1000,
		ClaimAuthorities: []*v1.ClaimAuthority{
			{Address: oap.Address(), Name: "Alec"},
		},
		DeadlineBlockHeight: 22291878 + 100,
	})
	if err != nil {
		log.Fatalf("Failed to create reward: %v", err)
	}
	fmt.Println("reward created at address: ", reward.Address)
}
