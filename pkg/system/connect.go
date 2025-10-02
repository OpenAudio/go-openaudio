package system

import (
	"context"
	"sync"

	"connectrpc.com/connect"
	coreV1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	storageV1 "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/system/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/system/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/core/server"
	storageServer "github.com/OpenAudio/go-openaudio/pkg/mediorum/server"
	"golang.org/x/sync/errgroup"
)

type SystemService struct {
	core    *server.CoreService
	storage *storageServer.StorageService
}

var _ v1connect.SystemServiceHandler = (*SystemService)(nil)

func NewSystemService(core *server.CoreService, storage *storageServer.StorageService) *SystemService {
	return &SystemService{core: core, storage: storage}
}

// GetHealth implements v1connect.SystemServiceHandler.
func (s *SystemService) GetHealth(ctx context.Context, req *connect.Request[v1.GetHealthRequest]) (*connect.Response[v1.GetHealthResponse], error) {
	res := &v1.GetHealthResponse{Status: "up"}

	var wg sync.WaitGroup

	wg.Add(3)

	go func() {
		defer wg.Done()
		coreHealth, err := s.core.GetHealth(ctx, connect.NewRequest(&coreV1.GetHealthRequest{}))
		if err != nil {
			return
		}
		res.CoreHealth = coreHealth.Msg
	}()

	go func() {
		defer wg.Done()
		storageHealth, err := s.storage.GetHealth(ctx, connect.NewRequest(&storageV1.GetHealthRequest{}))
		if err != nil {
			return
		}
		res.StorageHealth = storageHealth.Msg
	}()

	wg.Wait()

	return connect.NewResponse(res), nil
}

// Ping implements v1connect.SystemServiceHandler.
func (s *SystemService) Ping(ctx context.Context, _req *connect.Request[v1.PingRequest]) (*connect.Response[v1.PingResponse], error) {
	res := &v1.PingResponse{Message: "pong"}

	g := errgroup.Group{}

	g.Go(func() error {
		corePing, err := s.core.Ping(ctx, connect.NewRequest(&coreV1.PingRequest{}))
		if err != nil {
			return err
		}
		res.CorePing = corePing.Msg
		return nil
	})

	g.Go(func() error {
		storagePing, err := s.storage.Ping(ctx, connect.NewRequest(&storageV1.PingRequest{}))
		if err != nil {
			return err
		}
		res.StoragePing = storagePing.Msg
		return nil
	})

	err := g.Wait()
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(res), nil
}
