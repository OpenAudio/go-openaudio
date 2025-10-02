package utils

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
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

func EnsureProtocol(endpoint string) string {
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return "http://" + endpoint
	}
	return endpoint
}

func WaitForDevnetHealthy(timeout time.Duration) error {
	timeoutChan := time.After(timeout)
	nodes := []*sdk.AudiusdSDK{
		DiscoveryOne,
		ContentOne,
		ContentTwo,
		ContentThree,
	}

	for {
		select {
		case <-timeoutChan:
			return errors.New("timed out waiting for devnet to be ready")
		default:
		}
		allReady := true
		for _, n := range nodes {
			status, err := n.Core.GetStatus(context.Background(), connect.NewRequest(&corev1.GetStatusRequest{}))
			if err != nil {
				allReady = false
				break
			} else if !status.Msg.Ready {
				allReady = false
				break
			}
		}
		if allReady {
			break
		}
		time.Sleep(2 * time.Second)
	}
	return nil
}
