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
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

// subscribes by tx hash, pubsub completes once tx
// is committed
type TransactionHashPubsub = pubsub.Pubsub[struct{}]

type Server struct {
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

func NewServer(config *config.Config, cconfig *cconfig.Config, logger *common.Logger, pool *pgxpool.Pool, ethService *eth.EthService, posChannel chan pos.PoSRequest) (*Server, error) {
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

	s := &Server{
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

func (s *Server) Start(ctx context.Context) error {
	g, _ := errgroup.WithContext(ctx)

	g.Go(s.startABCI)
	g.Go(s.startRegistryBridge)
	g.Go(s.startEchoServer)
	g.Go(s.startSyncTasks)
	g.Go(s.startPeerManager)
	g.Go(s.startCache)
	g.Go(s.startDataCompanion)
	g.Go(s.syncLogs)
	g.Go(s.startStateSync)
	s.z.Info("routines started")

	return g.Wait()
}

func (s *Server) setSelf(self corev1connect.CoreServiceClient) {
	s.self = self
}

func (s *Server) syncLogs() error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.z.Sync()
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down all services...")

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error { return s.httpServer.Shutdown(ctx) })
	g.Go(s.node.Stop)
	g.Go(func() error {
		s.grpcServer.GracefulStop()
		return nil
	})

	return g.Wait()
}
