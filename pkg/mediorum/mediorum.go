package mediorum

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "embed"

	"connectrpc.com/connect"
	ethv1 "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1"
	ethv1connect "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1/v1connect"
	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/OpenAudio/go-openaudio/pkg/httputil"
	"github.com/OpenAudio/go-openaudio/pkg/lifecycle"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/ethcontracts"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server"
	"github.com/OpenAudio/go-openaudio/pkg/pos"
	"github.com/OpenAudio/go-openaudio/pkg/registrar"
	"github.com/OpenAudio/go-openaudio/pkg/version"
	"github.com/ethereum/go-ethereum/crypto"
	"go.uber.org/zap"
	"golang.org/x/exp/slog"
)

func Run(lc *lifecycle.Lifecycle, logger *zap.Logger, posChannel chan pos.PoSRequest, storageService *server.StorageService, core *coreServer.CoreService, ethService ethv1connect.EthServiceHandler) error {
	if ethService == nil {
		return errors.New("ethService is required")
	}
	env := os.Getenv("OPENAUDIO_ENV")
	logger.Info("starting mediorum", zap.String("OPENAUDIO_ENV", env))

	return runMediorum(lc, logger, env, posChannel, storageService, core, ethService)
}

func runMediorum(lc *lifecycle.Lifecycle, logger *zap.Logger, mediorumEnv string, posChannel chan pos.PoSRequest, storageService *server.StorageService, core *coreServer.CoreService, ethService ethv1connect.EthServiceHandler) error {
	logger = logger.With(zap.String("service", "mediorum"))

	isProd := mediorumEnv == "prod"
	isStage := mediorumEnv == "stage"

	// Wait for eth service to be ready before fetching endpoints
	statusCtx, statusCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer statusCancel()

	for {
		statusResp, err := ethService.GetStatus(statusCtx, connect.NewRequest(&ethv1.GetStatusRequest{}))
		if err == nil && statusResp.Msg.Ready {
			break
		}
		select {
		case <-statusCtx.Done():
			return fmt.Errorf("timeout waiting for eth service to be ready")
		case <-time.After(time.Second):
			// Retry
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	endpointsResp, err := ethService.GetRegisteredEndpoints(ctx, connect.NewRequest(&ethv1.GetRegisteredEndpointsRequest{}))
	if err != nil {
		return fmt.Errorf("failed to get registered endpoints from eth service: %v", err)
	}

	// Filter endpoints by service type and convert to registrar.Peer format
	var peers, signers []registrar.Peer
	for _, ep := range endpointsResp.Msg.Endpoints {
		peer := registrar.Peer{
			Host:   httputil.RemoveTrailingSlash(strings.ToLower(ep.Endpoint)),
			Wallet: strings.ToLower(ep.DelegateWallet),
		}

		// Content nodes and validators are peers
		if strings.EqualFold(ep.ServiceType, "content-node") || strings.EqualFold(ep.ServiceType, "validator") {
			peers = append(peers, peer)
		}
	}

	logger.Info("fetched registered nodes", zap.Int("peers", len(peers)), zap.Int("signers", len(signers)))
	cfg := core.GetConfig()
	if cfg == nil {
		return errors.New("core service not ready - cannot get config")
	}

	if cfg.NodeEndpoint == "" {
		return errors.New("nodeEndpoint not configured")
	}
	if cfg.EthereumKey == nil {
		return errors.New("delegatePrivateKey not configured")
	}

	nodeEndpoint := cfg.NodeEndpoint
	privateKey := cfg.EthereumKey
	privateKeyHex := hex.EncodeToString(crypto.FromECDSA(privateKey))
	walletAddress := cfg.WalletAddress
	delegateOwnerWallet := os.Getenv("delegateOwnerWallet")
	if !strings.EqualFold(walletAddress, delegateOwnerWallet) {
		slog.Warn("incorrect delegateOwnerWallet env config", "incorrect", delegateOwnerWallet, "computed", walletAddress)
	}

	trustedNotifierID, err := strconv.Atoi(getenvWithDefault("trustedNotifierID", "1"))
	if err != nil {
		logger.Warn("failed to parse trustedNotifierID", zap.Error(err))
	}
	spID, err := ethcontracts.GetServiceProviderIdFromEndpoint(nodeEndpoint, walletAddress)
	if err != nil || spID == 0 {
		go func() {
			for range time.Tick(10 * time.Second) {
				logger.Warn("failed to recover spID - please register at https://dashboard.audius.org and restart the server", zap.Error(err))
			}
		}()
	}

	// set dev defaults
	replicationFactor := 4
	spOwnerWallet := walletAddress
	dir := fmt.Sprintf("/tmp/mediorum_dev_%d", spID)
	blobStoreDSN := ""
	moveFromBlobStoreDSN := ""

	notDev := isProd || isStage
	if notDev {
		replicationFactor = 4
		spOwnerWallet = os.Getenv("spOwnerWallet")
		dir = "/tmp/mediorum"
		// Support OPENAUDIO_STORAGE_DRIVER_URL with fallback to AUDIUS_STORAGE_DRIVER_URL for backwards compatibility
		blobStoreDSN = getenvWithDefault("OPENAUDIO_STORAGE_DRIVER_URL", os.Getenv("AUDIUS_STORAGE_DRIVER_URL"))
		// Support OPENAUDIO_STORAGE_DRIVER_URL_MOVE_FROM with fallback to AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM for backwards compatibility
		moveFromBlobStoreDSN = getenvWithDefault("OPENAUDIO_STORAGE_DRIVER_URL_MOVE_FROM", os.Getenv("AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM"))
	}

	// Repair configuration
	repairEnabled := getenvWithDefault("OPENAUDIO_REPAIR_ENABLED", "true") == "true"
	repairInterval := time.Hour
	if ri := os.Getenv("OPENAUDIO_REPAIR_INTERVAL"); ri != "" {
		if parsed, err := time.ParseDuration(ri); err == nil {
			repairInterval = parsed
		} else {
			logger.Warn("failed to parse OPENAUDIO_REPAIR_INTERVAL, using default 1h", zap.String("value", ri), zap.Error(err))
		}
	}
	repairQmCidsUseListIndex := os.Getenv("OPENAUDIO_REPAIR_QM_CIDS_USE_LIST_INDEX") == "true"

	config := server.MediorumConfig{
		Self: registrar.Peer{
			Host:   httputil.RemoveTrailingSlash(strings.ToLower(nodeEndpoint)),
			Wallet: strings.ToLower(walletAddress),
		},
		ListenPort:                "1991",
		Peers:                     peers,
		Signers:                   signers,
		ReplicationFactor:         replicationFactor,
		PrivateKey:                privateKeyHex,
		Dir:                       dir,
		PostgresDSN:               getenvWithDefault("dbUrl", "postgres://postgres:postgres@db:5432/audius_creator_node"),
		BlobStoreDSN:              blobStoreDSN,
		MoveFromBlobStoreDSN:      moveFromBlobStoreDSN,
		TrustedNotifierID:         trustedNotifierID,
		SPID:                      spID,
		SPOwnerWallet:             spOwnerWallet,
		GitSHA:                    os.Getenv("GIT_SHA"),
		AudiusDockerCompose:       os.Getenv("AUDIUS_DOCKER_COMPOSE_GIT_SHA"),
		AutoUpgradeEnabled:        os.Getenv("autoUpgradeEnabled") == "true",
		StoreAll:                  os.Getenv("STORE_ALL") == "true",
		VersionJson:               version.Version,
		DiscoveryListensEndpoints: discoveryListensEndpoints(),
		LogLevel:                  getenvWithDefault("OPENAUDIO_LOG_LEVEL", "info"),
		DeadHosts:                 []string{},
		RepairEnabled:            repairEnabled,
		RepairInterval:           repairInterval,
		RepairQmCidsUseListIndex: repairQmCidsUseListIndex,
		BlobStorageStreaming:      os.Getenv("OPENAUDIO_BLOB_STORAGE_STREAMING") == "true",
	}

	ss, err := server.New(lc, logger, config, posChannel, core, ethService)
	if err != nil {
		return fmt.Errorf("failed to create server: %v", err)
	}

	storageService.SetMediorum(ss)
	return ss.MustStart()
}

func getenvWithDefault(key string, fallback string) string {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	return val
}

func discoveryListensEndpoints() []string {
	endpoints := os.Getenv("discoveryListensEndpoints")
	if endpoints == "" {
		return []string{}
	}
	return strings.Split(endpoints, ",")
}
