package sdk

import (
	"context"
	"crypto/ecdsa"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	ethv1connect "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1/v1connect"
	storagev1connect "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1/v1connect"
	systemv1connect "github.com/OpenAudio/go-openaudio/pkg/api/system/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/sdk/mediorum"
	"github.com/OpenAudio/go-openaudio/pkg/sdk/rewards"
)

type OpenAudioSDK struct {
	privKey *ecdsa.PrivateKey
	chainID string
	baseURL string

	Core    corev1connect.CoreServiceClient
	Storage *StorageServiceClientWithTUS
	System  systemv1connect.SystemServiceClient
	Eth     ethv1connect.EthServiceClient

	// helper instances
	Rewards  *rewards.Rewards
	Mediorum *mediorum.Mediorum
}

func ensureURLProtocol(url string) string {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "https://" + url
	}
	return url
}

func NewOpenAudioSDK(nodeURL string) *OpenAudioSDK {
	httpClient := http.DefaultClient
	url := ensureURLProtocol(nodeURL)

	coreClient := corev1connect.NewCoreServiceClient(httpClient, url)
	storageClientBase := storagev1connect.NewStorageServiceClient(httpClient, url)
	systemClient := systemv1connect.NewSystemServiceClient(httpClient, url)
	ethClient := ethv1connect.NewEthServiceClient(httpClient, url)
	mediorumClient := mediorum.NewWithCore(url, coreClient)
	rewardsClient := rewards.NewRewards(coreClient)

	sdk := &OpenAudioSDK{
		baseURL: url,
		Core:    coreClient,
		Storage: &StorageServiceClientWithTUS{
			StorageServiceClient: storageClientBase,
			sdk:                  nil, // Will be set below
		},
		System:   systemClient,
		Eth:      ethClient,
		Mediorum: mediorumClient,
		Rewards:  rewardsClient,
	}

	// Set SDK reference for the storage wrapper
	sdk.Storage.sdk = sdk

	return sdk
}

func (s *OpenAudioSDK) Init(ctx context.Context) error {
	nodeInfoResp, err := s.Core.GetNodeInfo(ctx, connect.NewRequest(&corev1.GetNodeInfoRequest{}))
	if err != nil {
		return err
	}

	s.chainID = nodeInfoResp.Msg.Chainid
	return nil
}

func (s *OpenAudioSDK) ChainID() string {
	return s.chainID
}

func (s *OpenAudioSDK) getServerURL() string {
	return s.baseURL
}
