package utils

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
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

// NewTestHTTPClient creates an HTTP client configured for local devnet testing.
// It skips TLS verification to work with self-signed certificates while maintaining HTTPS protocol.
func NewTestHTTPClient() *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Second,
	}
}

// NewTestSDK creates a new SDK instance with the test HTTP client.
// Use this when you need to create SDK instances in tests instead of using the pre-configured ones.
func NewTestSDK(nodeURL string) *sdk.OpenAudioSDK {
	return sdk.NewOpenAudioSDKWithClient(nodeURL, NewTestHTTPClient())
}

func init() {
	// Use custom HTTP client that skips TLS verification for self-signed certs in devnet
	// This maintains HTTPS protocol (as expected by the server) but allows local testing
	httpClient := NewTestHTTPClient()
	DiscoveryOne = sdk.NewOpenAudioSDKWithClient(DiscoveryOneRPC, httpClient)
	ContentOne = sdk.NewOpenAudioSDKWithClient(ContentOneRPC, httpClient)
	ContentTwo = sdk.NewOpenAudioSDKWithClient(ContentTwoRPC, httpClient)
	ContentThree = sdk.NewOpenAudioSDKWithClient(ContentThreeRPC, httpClient)
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

func WaitForDevnetHealthy(timeout ...time.Duration) error {
	timeoutDuration := 60 * time.Second
	if len(timeout) > 0 {
		timeoutDuration = timeout[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutDuration)
	defer cancel()

	nodes := []*sdk.OpenAudioSDK{
		DiscoveryOne,
		ContentOne,
		ContentTwo,
		ContentThree,
	}

	nodeAddresses := []string{
		ContentOneRPC,
		ContentTwoRPC,
		ContentThreeRPC,
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	client := NewTestHTTPClient()

	for {
		select {
		case <-ctx.Done():
			return errors.New("timed out waiting for devnet to be ready")
		case <-ticker.C:
			// Check core services are ready
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
			if !allReady {
				continue
			}

			// Check mediorum services have wallets registered
			allMediorumReady := true
			var healthResponse struct {
				Storage struct {
					WalletIsRegistered bool `json:"wallet_is_registered"`
				} `json:"storage"`
			}

			for _, addr := range nodeAddresses {
				// Ensure https:// protocol
				baseURL := addr
				if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
					baseURL = "https://" + baseURL
				} else if strings.HasPrefix(baseURL, "http://") {
					baseURL = strings.Replace(baseURL, "http://", "https://", 1)
				}

				req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/health-check", nil)
				if err != nil {
					allMediorumReady = false
					break
				}

				resp, err := client.Do(req)
				if err != nil {
					allMediorumReady = false
					break
				}

				if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&healthResponse) != nil || !healthResponse.Storage.WalletIsRegistered {
					resp.Body.Close()
					allMediorumReady = false
					break
				}
				resp.Body.Close()
			}

			if allReady && allMediorumReady {
				return nil
			}
		}
	}
}
