package mediorum

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "embed"

	"connectrpc.com/connect"
	ethv1 "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1"
	ethv1connect "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1/v1connect"
	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/OpenAudio/go-openaudio/pkg/env"
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
	mediorumEnv := env.String("OPENAUDIO_ENV")
	logger.Info("starting mediorum", zap.String("OPENAUDIO_ENV", mediorumEnv))

	return runMediorum(lc, logger, mediorumEnv, posChannel, storageService, core, ethService)
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
	delegateOwnerWallet := env.String("OPENAUDIO_DELEGATE_WALLET", "delegateOwnerWallet")
	if !strings.EqualFold(walletAddress, delegateOwnerWallet) {
		slog.Warn("incorrect delegateOwnerWallet env config", "incorrect", delegateOwnerWallet, "computed", walletAddress)
	}

	trustedNotifierID, err := strconv.Atoi(env.Get("1", "OPENAUDIO_TRUSTED_NOTIFIER_ID", "trustedNotifierID"))
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
	archiveBlobStoreDSN := ""
	moveFromBlobStoreDSN := ""

	notDev := isProd || isStage
	if notDev {
		replicationFactor = 4
		spOwnerWallet = env.String("OPENAUDIO_OWNER_WALLET", "spOwnerWallet")
		dir = "/tmp/mediorum"
		blobStoreDSN = env.String("OPENAUDIO_STORAGE_DRIVER_URL", "AUDIUS_STORAGE_DRIVER_URL")
		archiveBlobStoreDSN = env.String("OPENAUDIO_ARCHIVE_STORAGE_DRIVER_URL", "AUDIUS_ARCHIVE_STORAGE_DRIVER_URL")
		moveFromBlobStoreDSN = env.String("OPENAUDIO_STORAGE_DRIVER_URL_MOVE_FROM", "AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM")
	}

	// Repair configuration
	repairEnabled := env.Get("true", "OPENAUDIO_REPAIR_ENABLED") == "true"
	repairInterval := env.GetDuration(time.Hour, "OPENAUDIO_REPAIR_INTERVAL")
	repairConcurrency := env.GetInt(1, "OPENAUDIO_REPAIR_CONCURRENCY")
	storeRecentTTL := parseStoreRecentTTL(env.String("OPENAUDIO_STORE_RECENT_TTL"))

	// Archive mode keeps all history (no ops pruning). Otherwise the append-only
	// crudr "ops" table is pruned, retaining 30 days by default.
	archive := env.Get("false", "OPENAUDIO_ARCHIVE", "archive") == "true"
	opsRetention := env.GetDuration(server.DefaultOpsRetention, "OPENAUDIO_OPS_RETENTION")
	opsPruneInterval := env.GetDuration(6*time.Hour, "OPENAUDIO_OPS_PRUNE_INTERVAL")

	config := server.MediorumConfig{
		Self: registrar.Peer{
			Host:   httputil.RemoveTrailingSlash(strings.ToLower(nodeEndpoint)),
			Wallet: strings.ToLower(walletAddress),
		},
		ListenPort:           "1991",
		Peers:                peers,
		Signers:              signers,
		ReplicationFactor:    replicationFactor,
		PrivateKey:           privateKeyHex,
		Dir:                  dir,
		PostgresDSN:          env.Get("postgres://postgres:postgres@db:5432/audius_creator_node", "OPENAUDIO_DB_URL", "dbUrl"),
		BlobStoreDSN:         blobStoreDSN,
		ArchiveBlobStoreDSN:  archiveBlobStoreDSN,
		MoveFromBlobStoreDSN: moveFromBlobStoreDSN,
		TrustedNotifierID:    trustedNotifierID,
		SPID:                 spID,
		SPOwnerWallet:        spOwnerWallet,
		GitSHA:               env.String("OPENAUDIO_GIT_SHA", "GIT_SHA"),
		AudiusDockerCompose:  env.String("OPENAUDIO_DOCKER_COMPOSE_GIT_SHA", "AUDIUS_DOCKER_COMPOSE_GIT_SHA"),
		AutoUpgradeEnabled:   env.Bool("OPENAUDIO_AUTO_UPGRADE_ENABLED", "autoUpgradeEnabled"),
		// Core writes default on. The 1.7.0 rollout halted mainnet at height
		// 28173294 because PrepareProposal ignored the MaxTxBytes budget once
		// mediorum-op inflow backlogged validator mempools; proposals are now
		// byte-capped (capProposalTxs), so a backlog can defer txs but can no
		// longer stall block production. Operators can opt out with
		// OPENAUDIO_MEDIORUM_CORE_WRITES_ENABLED=false.
		CoreWritesEnabled:         env.Get("true", "OPENAUDIO_MEDIORUM_CORE_WRITES_ENABLED") == "true",
		StoreAll:                  env.Bool("OPENAUDIO_STORE_ALL", "STORE_ALL"),
		StoreRecent:               env.Bool("OPENAUDIO_STORE_RECENT"),
		StoreRecentTTL:            storeRecentTTL,
		VersionJson:               version.Version,
		DiscoveryListensEndpoints: discoveryListensEndpoints(),
		LogLevel:                  env.Get("info", "OPENAUDIO_LOG_LEVEL"),
		DeadHosts:                 []string{},
		RepairEnabled:             repairEnabled,
		RepairInterval:            repairInterval,
		RepairConcurrency:         repairConcurrency,
		Archive:                   archive,
		OpsRetention:              opsRetention,
		OpsPruneInterval:          opsPruneInterval,
		BlobStorageStreaming:      env.Bool("OPENAUDIO_BLOB_STORAGE_STREAMING"),
	}

	ss, err := server.New(lc, logger, config, posChannel, core, ethService)
	if err != nil {
		return fmt.Errorf("failed to create server: %v", err)
	}

	storageService.SetMediorum(ss)
	return ss.MustStart()
}

func discoveryListensEndpoints() []string {
	endpoints := env.String("OPENAUDIO_DISCOVERY_LISTENS_ENDPOINTS", "discoveryListensEndpoints")
	if endpoints == "" {
		return []string{}
	}
	return strings.Split(endpoints, ",")
}

func parseStoreRecentTTL(raw string) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return server.DefaultStoreRecentTTL
	}
	d, err := parseStoreRecentDuration(raw)
	if err != nil {
		return server.DefaultStoreRecentTTL
	}
	return d
}

func parseStoreRecentDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasSuffix(raw, "d") || strings.HasSuffix(raw, "D") {
		days, err := strconv.ParseFloat(strings.TrimSpace(raw[:len(raw)-1]), 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(raw)
}
