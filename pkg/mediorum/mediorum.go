package mediorum

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "embed"

	"github.com/AudiusProject/audiusd/pkg/common"
	coreServer "github.com/AudiusProject/audiusd/pkg/core/server"
	"github.com/AudiusProject/audiusd/pkg/httputil"
	"github.com/AudiusProject/audiusd/pkg/mediorum/ethcontracts"
	"github.com/AudiusProject/audiusd/pkg/mediorum/server"
	"github.com/AudiusProject/audiusd/pkg/pos"
	"github.com/AudiusProject/audiusd/pkg/registrar"
	"github.com/AudiusProject/audiusd/pkg/version"
	"golang.org/x/exp/slog"
	"golang.org/x/sync/errgroup"
)

func init() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{AddSource: true}))
	slog.SetDefault(logger)
}

func Run(ctx context.Context, logger *common.Logger, posChannel chan pos.PoSRequest, storageService *server.StorageService, core *coreServer.CoreService) error {
	mediorumEnv := os.Getenv("MEDIORUM_ENV")
	slog.Info("starting", "MEDIORUM_ENV", mediorumEnv)

	return runMediorum(mediorumEnv, posChannel, storageService, core, logger)
}

func runMediorum(mediorumEnv string, posChannel chan pos.PoSRequest, storageService *server.StorageService, core *coreServer.CoreService, commonLogger *common.Logger) error {
	logger := slog.With("creatorNodeEndpoint", os.Getenv("creatorNodeEndpoint"))

	isProd := mediorumEnv == "prod"
	isStage := mediorumEnv == "stage"
	isDev := mediorumEnv == "dev"

	var g registrar.PeerProvider
	if isProd {
		g = registrar.NewMultiProd()
	}
	if isStage {
		g = registrar.NewMultiStaging()
	}
	if isDev {
		g = registrar.NewMultiDev()
	}

	var peers, signers []registrar.Peer
	var err error

	eg := new(errgroup.Group)
	eg.Go(func() error {
		peers, err = g.Peers()
		return err
	})
	eg.Go(func() error {
		signers, err = g.Signers()
		return err
	})
	if err := eg.Wait(); err != nil {
		panic(err)
	}
	logger.Info("fetched registered nodes", "peers", len(peers), "signers", len(signers))

	creatorNodeEndpoint := os.Getenv("creatorNodeEndpoint")
	if creatorNodeEndpoint == "" {
		return errors.New("missing required env variable 'creatorNodeEndpoint'")
	}
	privateKeyHex := os.Getenv("delegatePrivateKey")
	if privateKeyHex == "" {
		return errors.New("missing required env variable 'delegatePrivateKey'")
	}

	privateKey, err := ethcontracts.ParsePrivateKeyHex(privateKeyHex)
	if err != nil {
		return fmt.Errorf("invalid private key: %v", err)
	}

	// compute wallet address
	walletAddress := ethcontracts.ComputeAddressFromPrivateKey(privateKey)
	delegateOwnerWallet := os.Getenv("delegateOwnerWallet")
	if !strings.EqualFold(walletAddress, delegateOwnerWallet) {
		slog.Warn("incorrect delegateOwnerWallet env config", "incorrect", delegateOwnerWallet, "computed", walletAddress)
	}

	trustedNotifierID, err := strconv.Atoi(getenvWithDefault("trustedNotifierID", "1"))
	if err != nil {
		logger.Warn("failed to parse trustedNotifierID", "err", err)
	}
	spID, err := ethcontracts.GetServiceProviderIdFromEndpoint(creatorNodeEndpoint, walletAddress)
	if err != nil || spID == 0 {
		go func() {
			for range time.Tick(10 * time.Second) {
				logger.Warn("failed to recover spID - please register at https://dashboard.audius.org and restart the server", "err", err)
			}
		}()
	}

	// set dev defaults
	replicationFactor := 3
	spOwnerWallet := walletAddress
	dir := fmt.Sprintf("/tmp/mediorum_dev_%d", spID)
	blobStoreDSN := ""
	moveFromBlobStoreDSN := ""

	notDev := isProd || isStage
	if notDev {
		replicationFactor = 3
		spOwnerWallet = os.Getenv("spOwnerWallet")
		dir = "/tmp/mediorum"
		blobStoreDSN = os.Getenv("AUDIUS_STORAGE_DRIVER_URL")
		moveFromBlobStoreDSN = os.Getenv("AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM")
	}

	config := server.MediorumConfig{
		Self: registrar.Peer{
			Host:   httputil.RemoveTrailingSlash(strings.ToLower(creatorNodeEndpoint)),
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
		LogLevel:                  getenvWithDefault("AUDIUSD_LOG_LEVEL", "info"),
	}

	ss, err := server.New(config, g, posChannel, core, commonLogger)
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
