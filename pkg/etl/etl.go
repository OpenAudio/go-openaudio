package etl

import (
	"context"

	"connectrpc.com/connect"
	"github.com/OpenAudio/go-openaudio/etl/db"
	em "github.com/OpenAudio/go-openaudio/etl/processors/entity_manager"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Indexer extracts blockchain data from a Core RPC and indexes it into PostgreSQL.
type Indexer struct {
	dbURL               string
	runDownMigrations   bool
	skipMigrations      bool
	startingBlockHeight int64
	endingBlockHeight   int64
	checkReadiness      bool
	ChainID             string
	config      Config
	lastEmBlock int64 // last assigned blocks.number; incremented only for blocks with EM txs

	core       corev1connect.CoreServiceClient
	pool       *pgxpool.Pool
	db         *db.Queries
	logger     *zap.Logger
	dispatcher *em.Dispatcher

	blockPubsub *BlockPubsub
	playPubsub  *PlayPubsub

	mvRefresher *MaterializedViewRefresher
}

// New creates a new ETL indexer.
func New(core corev1connect.CoreServiceClient, logger *zap.Logger) *Indexer {
	return &Indexer{
		logger: logger.With(zap.String("service", "etl")),
		core:   core,
		config: DefaultConfig(),
	}
}

// SetConfig sets optional component flags.
func (e *Indexer) SetConfig(c Config) {
	e.config = c
}

func (e *Indexer) SetDBURL(dbURL string) {
	e.dbURL = dbURL
}

func (e *Indexer) SetStartingBlockHeight(startingBlockHeight int64) {
	e.startingBlockHeight = startingBlockHeight
}

func (e *Indexer) SetEndingBlockHeight(endingBlockHeight int64) {
	e.endingBlockHeight = endingBlockHeight
}

func (e *Indexer) SetRunDownMigrations(runDownMigrations bool) {
	e.runDownMigrations = runDownMigrations
}

func (e *Indexer) SetSkipMigrations(skip bool) {
	e.skipMigrations = skip
}

func (e *Indexer) SetCheckReadiness(checkReadiness bool) {
	e.checkReadiness = checkReadiness
}

func (e *Indexer) GetDB() *db.Queries {
	return e.db
}

// GetBlockPubsub returns the block pubsub instance.
func (e *Indexer) GetBlockPubsub() *BlockPubsub {
	return e.blockPubsub
}

// GetPlayPubsub returns the play pubsub instance.
func (e *Indexer) GetPlayPubsub() *PlayPubsub {
	return e.playPubsub
}

// InitializeChainID fetches and caches the chain ID from the core service.
func (e *Indexer) InitializeChainID(ctx context.Context) error {
	nodeInfoResp, err := e.core.GetNodeInfo(ctx, connect.NewRequest(&corev1.GetNodeInfoRequest{}))
	if err != nil {
		e.ChainID = "--"
		e.logger.Warn("Failed to get chain ID from core service, using fallback", zap.Error(err), zap.String("chainID", e.ChainID))
		return nil
	}

	e.ChainID = nodeInfoResp.Msg.Chainid
	e.logger.Info("Initialized chain ID", zap.String("chainID", e.ChainID))
	return nil
}
