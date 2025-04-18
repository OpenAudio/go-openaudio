package utils

import (
	"log"
	"os"

	"github.com/AudiusProject/audiusd/pkg/core/sdk"
)

var (
	discoveryOneGrpc = getEnvWithDefault("discoveryOneGRPC", "")
	discoveryOneJrpc = getEnvWithDefault("discoveryOneJRPC", "")
	discoveryOneOapi = getEnvWithDefault("discoveryOneOAPI", "")

	contentOneGrpc = getEnvWithDefault("contentOneGRPC", "")
	contentOneJrpc = getEnvWithDefault("contentOneJRPC", "")
	contentOneOapi = getEnvWithDefault("contentOneOAPI", "")

	contentTwoGrpc = getEnvWithDefault("contentTwoGRPC", "")
	contentTwoJrpc = getEnvWithDefault("contentTwoJRPC", "")
	contentTwoOapi = getEnvWithDefault("contentTwoOAPI", "")

	contentThreeGrpc = getEnvWithDefault("contentThreeGRPC", "")
	contentThreeJrpc = getEnvWithDefault("contentThreeJRPC", "")
	contentThreeOapi = getEnvWithDefault("contentThreeOAPI", "")

	DiscoveryOne = newTestSdk(discoveryOneGrpc, discoveryOneJrpc, discoveryOneOapi)
	ContentOne   = newTestSdk(contentOneGrpc, contentOneJrpc, contentOneOapi)
	ContentTwo   = newTestSdk(contentTwoGrpc, contentTwoJrpc, contentTwoOapi)
	ContentThree = newTestSdk(contentThreeGrpc, contentThreeJrpc, contentThreeOapi)
)

func getEnvWithDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func newTestSdk(grpc, jrpc, oapi string) *sdk.Sdk {
	if grpc == "" || jrpc == "" || oapi == "" {
		return nil
	}
	node, err := sdk.NewSdk(sdk.WithGrpcendpoint(grpc), sdk.WithJrpcendpoint(jrpc), sdk.WithOapiendpoint(oapi))
	if err != nil {
		log.Panicf("node init error %s %s: %v", grpc, jrpc, err)
	}
	return node
}
