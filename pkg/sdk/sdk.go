package sdk

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	etlv1connect "github.com/OpenAudio/go-openaudio/pkg/api/etl/v1/v1connect"
	ethv1connect "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1/v1connect"
	storagev1connect "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1/v1connect"
	systemv1connect "github.com/OpenAudio/go-openaudio/pkg/api/system/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/sdk/mediorum"
	"github.com/OpenAudio/go-openaudio/pkg/sdk/rewards"
	"github.com/bdragon300/tusgo"
)

type OpenAudioSDK struct {
	privKey *ecdsa.PrivateKey
	chainID string
	baseURL string

	Core    corev1connect.CoreServiceClient
	Storage *StorageServiceClientWithTUS
	ETL     etlv1connect.ETLServiceClient
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
	sdk := NewOpenAudioSDKWithClient(nodeURL, http.DefaultClient)
	// Override Mediorum to ensure default timeout is still applied separately
	sdk.Mediorum = mediorum.New(sdk.baseURL, mediorum.WithCoreClient(sdk.Core))
	return sdk
}

func NewOpenAudioSDKWithClient(nodeURL string, httpClient *http.Client) *OpenAudioSDK {
	baseURL := ensureURLProtocol(nodeURL)

	coreClient := corev1connect.NewCoreServiceClient(httpClient, baseURL)
	storageClientBase := storagev1connect.NewStorageServiceClient(httpClient, baseURL)
	etlClient := etlv1connect.NewETLServiceClient(httpClient, baseURL)
	systemClient := systemv1connect.NewSystemServiceClient(httpClient, baseURL)
	ethClient := ethv1connect.NewEthServiceClient(httpClient, baseURL)
	mediorumClient := mediorum.New(baseURL, mediorum.WithCoreClient(coreClient), mediorum.WithHTTPClient(httpClient))
	rewardsClient := rewards.NewRewards(coreClient)

	// Initialize TUS client
	tusBaseURL, err := url.Parse(fmt.Sprintf("%s/files/", baseURL))
	if err != nil {
		panic(fmt.Errorf("invalid base URL: %w", err))
	}
	tusClient := tusgo.NewClient(httpClient, tusBaseURL)
	tusClient.Capabilities = &tusgo.ServerCapabilities{
		Extensions:       []string{"creation", "creation-with-upload", "termination"},
		ProtocolVersions: []string{"1.0.0"},
	}

	sdk := &OpenAudioSDK{
		baseURL: baseURL,
		Core:    coreClient,
		Storage: &StorageServiceClientWithTUS{
			StorageServiceClient: storageClientBase,
			tusClient:            tusClient,
		},
		ETL:      etlClient,
		System:   systemClient,
		Eth:      ethClient,
		Mediorum: mediorumClient,
		Rewards:  rewardsClient,
	}

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
