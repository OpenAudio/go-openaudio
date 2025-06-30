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
)

type AudiusdSDK struct {
	privKey *ecdsa.PrivateKey

	Core    corev1connect.CoreServiceClient
	Storage storagev1connect.StorageServiceClient
	ETL     etlv1connect.ETLServiceClient
	System  systemv1connect.SystemServiceClient
	Eth     ethv1connect.EthServiceClient
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
	sdk := &AudiusdSDK{
		Core:    corev1connect.NewCoreServiceClient(httpClient, url),
		Storage: storagev1connect.NewStorageServiceClient(httpClient, url),
		ETL:     etlv1connect.NewETLServiceClient(httpClient, url),
		System:  systemv1connect.NewSystemServiceClient(httpClient, url),
		Eth:     ethv1connect.NewEthServiceClient(httpClient, url),
	}

	return sdk
}
