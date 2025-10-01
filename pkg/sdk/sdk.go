package sdk

import (
	"crypto/ecdsa"
	"net/http"
	"strings"

	corev1connect "github.com/AudiusProject/audiusd/pkg/api/core/v1/v1connect"
	ethv1connect "github.com/AudiusProject/audiusd/pkg/api/eth/v1/v1connect"
	etlv1connect "github.com/AudiusProject/audiusd/pkg/api/etl/v1/v1connect"
	storagev1connect "github.com/AudiusProject/audiusd/pkg/api/storage/v1/v1connect"
	systemv1connect "github.com/AudiusProject/audiusd/pkg/api/system/v1/v1connect"
	"github.com/AudiusProject/audiusd/pkg/sdk/rewards"
)

type AudiusdSDK struct {
	privKey *ecdsa.PrivateKey

	Core    corev1connect.CoreServiceClient
	Storage storagev1connect.StorageServiceClient
	ETL     etlv1connect.ETLServiceClient
	System  systemv1connect.SystemServiceClient
	Eth     ethv1connect.EthServiceClient

	// helper instances
	Rewards *rewards.Rewards
}

func ensureURLProtocol(url string) string {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "https://" + url
	}
	return url
}

func NewAudiusdSDK(nodeURL string) *AudiusdSDK {
	httpClient := http.DefaultClient
	url := ensureURLProtocol(nodeURL)

	coreClient := corev1connect.NewCoreServiceClient(httpClient, url)
	storageClient := storagev1connect.NewStorageServiceClient(httpClient, url)
	etlClient := etlv1connect.NewETLServiceClient(httpClient, url)
	systemClient := systemv1connect.NewSystemServiceClient(httpClient, url)
	ethClient := ethv1connect.NewEthServiceClient(httpClient, url)
	rewardsClient := rewards.NewRewards(coreClient)

	sdk := &AudiusdSDK{
		Core:    coreClient,
		Storage: storageClient,
		ETL:     etlClient,
		System:  systemClient,
		Eth:     ethClient,
		Rewards: rewardsClient,
	}

	return sdk
}
