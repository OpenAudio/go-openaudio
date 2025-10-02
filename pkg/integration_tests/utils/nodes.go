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
	DiscoveryOneRPC = getEnvWithDefault("discoveryOneRPC", "node1.oap.devnet")
	ContentOneRPC   = getEnvWithDefault("contentOneRPC", "node2.oap.devnet")
	ContentTwoRPC   = getEnvWithDefault("contentTwoRPC", "node3.oap.devnet")
	ContentThreeRPC = getEnvWithDefault("contentThreeRPC", "node4.oap.devnet")

	DiscoveryOne *sdk.OpenAudioSDK
	ContentOne   *sdk.OpenAudioSDK
	ContentTwo   *sdk.OpenAudioSDK
	ContentThree *sdk.OpenAudioSDK
)

func init() {
	DiscoveryOne = sdk.NewOpenAudioSDK(DiscoveryOneRPC)
	ContentOne = sdk.NewOpenAudioSDK(ContentOneRPC)
	ContentTwo = sdk.NewOpenAudioSDK(ContentTwoRPC)
	ContentThree = sdk.NewOpenAudioSDK(ContentThreeRPC)
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
	nodes := []*sdk.OpenAudioSDK{
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
