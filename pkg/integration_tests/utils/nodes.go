package utils

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
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

	devnetReadyMu  sync.Mutex
	devnetReadyErr error
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
	timeoutDuration := 300 * time.Second
	if len(timeout) > 0 {
		timeoutDuration = timeout[0]
		return waitForDevnetHealthy(timeoutDuration)
	}

	devnetReadyMu.Lock()
	err := devnetReadyErr
	devnetReadyMu.Unlock()
	if err != nil {
		return err
	}

	err = waitForDevnetHealthy(timeoutDuration)
	if err != nil {
		devnetReadyMu.Lock()
		if devnetReadyErr == nil {
			devnetReadyErr = err
		}
		err = devnetReadyErr
		devnetReadyMu.Unlock()
	}
	return err
}

func waitForDevnetHealthy(timeoutDuration time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutDuration)
	defer cancel()

	nodes := []struct {
		name string
		sdk  *sdk.OpenAudioSDK
	}{
		{DiscoveryOneRPC, DiscoveryOne},
		{ContentOneRPC, ContentOne},
		{ContentTwoRPC, ContentTwo},
		{ContentThreeRPC, ContentThree},
	}

	storageNodes := []string{
		ContentOneRPC,
		ContentTwoRPC,
		ContentThreeRPC,
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Use a short per-request timeout so a single slow/hung node cannot
	// exhaust the overall readiness budget on one iteration.
	pollClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}

	checkReady := func() error {
		for _, n := range nodes {
			reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
			status, err := n.sdk.Core.GetStatus(reqCtx, connect.NewRequest(&corev1.GetStatusRequest{}))
			reqCancel()
			if err != nil {
				return fmt.Errorf("%s core status: %w", n.name, err)
			}
			if status == nil || status.Msg == nil {
				return fmt.Errorf("%s core status: empty response", n.name)
			}
			if !status.Msg.Ready {
				return fmt.Errorf("%s core not ready", n.name)
			}
		}

		for _, addr := range storageNodes {
			baseURL := addr
			if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
				baseURL = "https://" + baseURL
			} else if strings.HasPrefix(baseURL, "http://") {
				baseURL = strings.Replace(baseURL, "http://", "https://", 1)
			}

			reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
			req, err := http.NewRequestWithContext(reqCtx, "GET", baseURL+"/health-check", nil)
			if err != nil {
				reqCancel()
				return fmt.Errorf("%s storage health request: %w", addr, err)
			}
			resp, err := pollClient.Do(req)
			if err != nil {
				reqCancel()
				return fmt.Errorf("%s storage health: %w", addr, err)
			}

			var healthResponse struct {
				Storage struct {
					WalletIsRegistered bool `json:"wallet_is_registered"`
				} `json:"storage"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&healthResponse)
			closeErr := resp.Body.Close()
			reqCancel()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s storage health returned %s", addr, resp.Status)
			}
			if decodeErr != nil {
				return fmt.Errorf("%s storage health decode: %w", addr, decodeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("%s storage health close: %w", addr, closeErr)
			}
			if !healthResponse.Storage.WalletIsRegistered {
				return fmt.Errorf("%s storage wallet not registered", addr)
			}
		}
		return nil
	}

	lastErr := checkReady()
	if lastErr == nil {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("timed out waiting for devnet to be ready: last check failed: %w", lastErr)
			}
			return errors.New("timed out waiting for devnet to be ready")
		case <-ticker.C:
			lastErr = checkReady()
			if lastErr == nil {
				return nil
			}
		}
	}
}
