package etlserver

import (
	"context"

	"connectrpc.com/connect"
	"github.com/OpenAudio/go-openaudio/etl"
	"github.com/OpenAudio/go-openaudio/etl/db"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/etl/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/etl/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/location"
	"go.uber.org/zap"
)

var _ v1connect.ETLServiceHandler = (*ETLService)(nil)

// ETLService is the go-openaudio ETL wrapper that composes the etl.Indexer
// with LocationService and implements the ETL ConnectRPC query handlers.
// Use this when running a full openaudio node with explorer.
type ETLService struct {
	indexer    *etl.Indexer
	locationDB *location.LocationService
	logger     *zap.Logger
}

// NewETLService creates an ETL service with indexer and location support.
func NewETLService(indexer *etl.Indexer, locationDB *location.LocationService, logger *zap.Logger) *ETLService {
	return &ETLService{
		indexer:    indexer,
		locationDB: locationDB,
		logger:     logger.With(zap.String("service", "etl-server")),
	}
}

// Run runs the indexer. Blocks until done or error.
func (s *ETLService) Run() error {
	return s.indexer.Run()
}

// GetDB returns the ETL database queries.
func (s *ETLService) GetDB() *db.Queries {
	return s.indexer.GetDB()
}

// GetBlockPubsub returns the block pubsub instance.
func (s *ETLService) GetBlockPubsub() *etl.BlockPubsub {
	return s.indexer.GetBlockPubsub()
}

// GetPlayPubsub returns the play pubsub instance.
func (s *ETLService) GetPlayPubsub() *etl.PlayPubsub {
	return s.indexer.GetPlayPubsub()
}

// GetLocationDB returns the location service instance.
func (s *ETLService) GetLocationDB() *location.LocationService {
	return s.locationDB
}

// ChainID returns the chain ID from the core service.
func (s *ETLService) ChainID() string {
	return s.indexer.ChainID
}

// Indexer returns the underlying indexer for direct access.
func (s *ETLService) Indexer() *etl.Indexer {
	return s.indexer
}

// SetRunDownMigrations delegates to the indexer.
func (s *ETLService) SetRunDownMigrations(v bool) {
	s.indexer.SetRunDownMigrations(v)
}

// ConnectRPC handlers (stubs - console queries DB directly via GetDB)

func (s *ETLService) Ping(context.Context, *connect.Request[v1.PingRequest]) (*connect.Response[v1.PingResponse], error) {
	return connect.NewResponse(&v1.PingResponse{}), nil
}

func (s *ETLService) GetHealth(context.Context, *connect.Request[v1.GetHealthRequest]) (*connect.Response[v1.GetHealthResponse], error) {
	return connect.NewResponse(&v1.GetHealthResponse{}), nil
}

func (s *ETLService) GetBlocks(context.Context, *connect.Request[v1.GetBlocksRequest]) (*connect.Response[v1.GetBlocksResponse], error) {
	return connect.NewResponse(&v1.GetBlocksResponse{}), nil
}

func (s *ETLService) GetTransactions(context.Context, *connect.Request[v1.GetTransactionsRequest]) (*connect.Response[v1.GetTransactionsResponse], error) {
	return connect.NewResponse(&v1.GetTransactionsResponse{}), nil
}

func (s *ETLService) GetPlays(context.Context, *connect.Request[v1.GetPlaysRequest]) (*connect.Response[v1.GetPlaysResponse], error) {
	return connect.NewResponse(&v1.GetPlaysResponse{}), nil
}

func (s *ETLService) GetManageEntities(context.Context, *connect.Request[v1.GetManageEntitiesRequest]) (*connect.Response[v1.GetManageEntitiesResponse], error) {
	return connect.NewResponse(&v1.GetManageEntitiesResponse{}), nil
}

func (s *ETLService) GetValidators(context.Context, *connect.Request[v1.GetValidatorsRequest]) (*connect.Response[v1.GetValidatorsResponse], error) {
	return connect.NewResponse(&v1.GetValidatorsResponse{}), nil
}

func (s *ETLService) GetLocation(context.Context, *connect.Request[v1.GetLocationRequest]) (*connect.Response[v1.GetLocationResponse], error) {
	return connect.NewResponse(&v1.GetLocationResponse{}), nil
}
