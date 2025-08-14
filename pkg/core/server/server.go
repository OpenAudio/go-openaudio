package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1connect "github.com/AudiusProject/audiusd/pkg/api/core/v1/v1connect"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	"github.com/AudiusProject/audiusd/pkg/eth"
	"github.com/AudiusProject/audiusd/pkg/lifecycle"
	aLogger "github.com/AudiusProject/audiusd/pkg/logger"
	"github.com/AudiusProject/audiusd/pkg/pos"
	"github.com/AudiusProject/audiusd/pkg/pubsub"
	"github.com/AudiusProject/audiusd/pkg/rewards"
	cconfig "github.com/cometbft/cometbft/config"
	nm "github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/rpc/client/local"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// subscribes by tx hash, pubsub completes once tx
// is committed
type TransactionHashPubsub = pubsub.Pubsub[struct{}]

type Server struct {
	lc             *lifecycle.Lifecycle
	config         *config.Config
	cometbftConfig *cconfig.Config
	logger         *common.Logger
	z              *zap.Logger
	self           corev1connect.CoreServiceClient
	eth            *eth.EthService

	httpServer         *echo.Echo
	grpcServer         *grpc.Server
	pool               *pgxpool.Pool
	mediorumPoSChannel chan pos.PoSRequest

	db    *db.Queries
	node  *nm.Node
	rpc   *local.Local
	mempl *Mempool

	peers   map[string]corev1connect.CoreServiceClient
	peersMU sync.RWMutex

	txPubsub *TransactionHashPubsub

	cache     *Cache
	abciState *ABCIState

	rewards *rewards.RewardAttester

	awaitHttpServerReady chan struct{}
	awaitRpcReady        chan struct{}
	awaitEthReady        chan struct{}
}

func NewServer(lc *lifecycle.Lifecycle, config *config.Config, cconfig *cconfig.Config, logger *common.Logger, pool *pgxpool.Pool, ethService *eth.EthService, posChannel chan pos.PoSRequest) (*Server, error) {
	// create mempool
	mempl := NewMempool(logger, config, db.New(pool), cconfig.Mempool.Size)

	// create pubsubs
	txPubsub := pubsub.NewPubsub[struct{}]()

	httpServer := echo.New()
	grpcServer := grpc.NewServer()

	baseLogger, err := aLogger.CreateLogger(config.Environment, config.LogLevel)
	if err != nil {
		return nil, fmt.Errorf("failed to create zap logger: %v", err)
	}

	z := baseLogger.With(zap.String("service", "core"), zap.String("node", config.NodeEndpoint))
	z.Info("core server starting")

	coreLifecycle := lifecycle.NewFromLifecycle(lc, "core")

	s := &Server{
		lc:             coreLifecycle,
		config:         config,
		cometbftConfig: cconfig,
		logger:         logger.Child("server"),
		z:              z,
		eth:            ethService,

		pool:               pool,
		mediorumPoSChannel: posChannel,

		db:        db.New(pool),
		mempl:     mempl,
		peers:     make(map[string]corev1connect.CoreServiceClient),
		txPubsub:  txPubsub,
		cache:     NewCache(config),
		abciState: NewABCIState(config.RetainHeight),

		httpServer: httpServer,
		grpcServer: grpcServer,

		rewards: rewards.NewRewardAttester(config.EthereumKey, config.Rewards),

		awaitHttpServerReady: make(chan struct{}),
		awaitRpcReady:        make(chan struct{}),
		awaitEthReady:        make(chan struct{}),
	}

	return s, nil
}

func (s *Server) Start() error {
	s.lc.AddManagedRoutine("abci", s.startABCI)
	s.lc.AddManagedRoutine("registry bridge", s.startRegistryBridge)
	s.lc.AddManagedRoutine("echo server", s.startEchoServer)
	s.lc.AddManagedRoutine("sync tasks", s.startSyncTasks)
	s.lc.AddManagedRoutine("peer manager", s.startPeerManager)
	s.lc.AddManagedRoutine("cache", s.startCache)
	s.lc.AddManagedRoutine("data companion", s.startDataCompanion)
	s.lc.AddManagedRoutine("log sync", s.syncLogs)
	s.lc.AddManagedRoutine("state sync", s.startStateSync)
	s.lc.AddManagedRoutine("mempool cache", s.startMempoolCache)

	s.z.Info("routines started")

	s.lc.Wait()
	return fmt.Errorf("core stopped or shut down")
}

func (s *Server) setSelf(self corev1connect.CoreServiceClient) {
	s.self = self
}

func (s *Server) syncLogs(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Second)
	for {
		select {
		case <-ticker.C:
			s.z.Sync()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Server) Shutdown() error {
	s.logger.Info("shutting down all services...")

	if err := s.lc.ShutdownWithTimeout(60 * time.Second); err != nil {
		return fmt.Errorf("failure shutting down core: %v", err)
	}
	s.grpcServer.GracefulStop()

	return nil
}
