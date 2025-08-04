package etl

import (
	"context"

	"connectrpc.com/connect"
	corev1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	corev1connect "github.com/AudiusProject/audiusd/pkg/api/core/v1/v1connect"
	v1 "github.com/AudiusProject/audiusd/pkg/api/etl/v1"
	"github.com/AudiusProject/audiusd/pkg/api/etl/v1/v1connect"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/etl/db"
	"github.com/AudiusProject/audiusd/pkg/etl/location"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ v1connect.ETLServiceHandler = (*ETLService)(nil)

type ETLService struct {
	dbURL               string
	runDownMigrations   bool
	startingBlockHeight int64
	endingBlockHeight   int64
	checkReadiness      bool
	ChainID             string

	core   corev1connect.CoreServiceClient
	pool   *pgxpool.Pool
	db     *db.Queries
	logger *common.Logger

	locationDB *location.LocationService

	blockPubsub *BlockPubsub
	playPubsub  *PlayPubsub

	mvRefresher *MaterializedViewRefresher
}

func NewETLService(core corev1connect.CoreServiceClient, logger *common.Logger) *ETLService {
	etl := &ETLService{
		logger: logger.Child("etl"),
		core:   core,
	}

	return etl
}

func (e *ETLService) SetDBURL(dbURL string) {
	e.dbURL = dbURL
}

func (e *ETLService) SetStartingBlockHeight(startingBlockHeight int64) {
	e.startingBlockHeight = startingBlockHeight
}

func (e *ETLService) SetEndingBlockHeight(endingBlockHeight int64) {
	e.endingBlockHeight = endingBlockHeight
}

func (e *ETLService) SetRunDownMigrations(runDownMigrations bool) {
	e.runDownMigrations = runDownMigrations
}

func (e *ETLService) SetCheckReadiness(checkReadiness bool) {
	e.checkReadiness = checkReadiness
}

func (e *ETLService) GetDB() *db.Queries {
	return e.db
}

// GetBlockPubsub returns the block pubsub instance
func (e *ETLService) GetBlockPubsub() *BlockPubsub {
	return e.blockPubsub
}

// GetPlayPubsub returns the play pubsub instance
func (e *ETLService) GetPlayPubsub() *PlayPubsub {
	return e.playPubsub
}

// GetLocationDB returns the location service instance
func (e *ETLService) GetLocationDB() *location.LocationService {
	return e.locationDB
}

// InitializeChainID fetches and caches the chain ID from the core service
func (e *ETLService) InitializeChainID(ctx context.Context) error {
	nodeInfoResp, err := e.core.GetNodeInfo(ctx, connect.NewRequest(&corev1.GetNodeInfoRequest{}))
	if err != nil {
		// Use fallback chain ID if core service is not available
		e.ChainID = "--"
		e.logger.Warn("Failed to get chain ID from core service, using fallback", "error", err, "chainID", e.ChainID)
		return nil
	}

	e.ChainID = nodeInfoResp.Msg.Chainid
	e.logger.Info("Initialized chain ID", "chainID", e.ChainID)
	return nil
}

// GetHealth implements v1connect.ETLServiceHandler.
func (e *ETLService) GetHealth(context.Context, *connect.Request[v1.GetHealthRequest]) (*connect.Response[v1.GetHealthResponse], error) {
	return connect.NewResponse(&v1.GetHealthResponse{}), nil
}

// GetBlocks implements v1connect.ETLServiceHandler.
func (e *ETLService) GetBlocks(context.Context, *connect.Request[v1.GetBlocksRequest]) (*connect.Response[v1.GetBlocksResponse], error) {
	return connect.NewResponse(&v1.GetBlocksResponse{}), nil
}

// GetLocation implements v1connect.ETLServiceHandler.
func (e *ETLService) GetLocation(context.Context, *connect.Request[v1.GetLocationRequest]) (*connect.Response[v1.GetLocationResponse], error) {
	return connect.NewResponse(&v1.GetLocationResponse{}), nil
}

// GetManageEntities implements v1connect.ETLServiceHandler.
func (e *ETLService) GetManageEntities(context.Context, *connect.Request[v1.GetManageEntitiesRequest]) (*connect.Response[v1.GetManageEntitiesResponse], error) {
	return connect.NewResponse(&v1.GetManageEntitiesResponse{}), nil
}

// GetPlays implements v1connect.ETLServiceHandler.
func (e *ETLService) GetPlays(context.Context, *connect.Request[v1.GetPlaysRequest]) (*connect.Response[v1.GetPlaysResponse], error) {
	return connect.NewResponse(&v1.GetPlaysResponse{}), nil
}

// GetTransactions implements v1connect.ETLServiceHandler.
func (e *ETLService) GetTransactions(context.Context, *connect.Request[v1.GetTransactionsRequest]) (*connect.Response[v1.GetTransactionsResponse], error) {
	return connect.NewResponse(&v1.GetTransactionsResponse{}), nil
}

// GetValidators implements v1connect.ETLServiceHandler.
func (e *ETLService) GetValidators(context.Context, *connect.Request[v1.GetValidatorsRequest]) (*connect.Response[v1.GetValidatorsResponse], error) {
	return connect.NewResponse(&v1.GetValidatorsResponse{}), nil
}

// Ping implements v1connect.ETLServiceHandler.
func (e *ETLService) Ping(context.Context, *connect.Request[v1.PingRequest]) (*connect.Response[v1.PingResponse], error) {
	return connect.NewResponse(&v1.PingResponse{}), nil
}
