package utils

import (
	"os"

	"github.com/AudiusProject/audiusd/pkg/sdk"
)

var (
	DiscoveryOneRPC = getEnvWithDefault("discoveryOneRPC", "node1.audiusd.devnet")
	ContentOneRPC   = getEnvWithDefault("contentOneRPC", "node2.audiusd.devnet")
	ContentTwoRPC   = getEnvWithDefault("contentTwoRPC", "node3.audiusd.devnet")
	ContentThreeRPC = getEnvWithDefault("contentThreeRPC", "node4.audiusd.devnet")

	DiscoveryOne *sdk.AudiusdSDK
	ContentOne   *sdk.AudiusdSDK
	ContentTwo   *sdk.AudiusdSDK
	ContentThree *sdk.AudiusdSDK
)

func init() {
	DiscoveryOne = sdk.NewAudiusdSDK(DiscoveryOneRPC)
	ContentOne = sdk.NewAudiusdSDK(ContentOneRPC)
	ContentTwo = sdk.NewAudiusdSDK(ContentTwoRPC)
	ContentThree = sdk.NewAudiusdSDK(ContentThreeRPC)
}

func getEnvWithDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
