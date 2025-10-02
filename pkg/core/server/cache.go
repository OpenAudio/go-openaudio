package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/lifecycle"
	"github.com/cometbft/cometbft/types"
	"github.com/maypok86/otter"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ProcessStateABCI           = "abci"
	ProcessStateRegistryBridge = "registryBridge"
	ProcessStateEchoServer     = "echoServer"
	ProcessStateSyncTasks      = "syncTasks"
	ProcessStatePeerManager    = "peerManager"
	ProcessStateDataCompanion  = "dataCompanion"
	ProcessStateCache          = "cache"
	ProcessStateLogSync        = "logSync"
	ProcessStateStateSync      = "stateSync"
	ProcessStateMempoolCache   = "mempoolCache"

	NodeInfoKey      = "nodeInfo"
	PeersKey         = "peers"
	ChainInfoKey     = "chainInfo"
	SyncInfoKey      = "syncInfo"
	PruningInfoKey   = "pruningInfo"
	ResourceInfoKey  = "resourceInfo"
	ValidatorInfoKey = "validatorInfo"
	MempoolInfoKey   = "mempoolInfo"
	SnapshotInfoKey  = "snapshotInfo"
)

// a simple in memory cache of frequently queried things
// maybe upgrade to something like bigcache later
type Cache struct {
	// old values, replace with otter cache later
	currentHeight  atomic.Int64
	currentTxCount atomic.Int64
	catchingUp     atomic.Bool

	// process states
	abciState           otter.Cache[string, *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo]
	registryBridgeState otter.Cache[string, *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo]
	echoServerState     otter.Cache[string, *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo]
	syncTasksState      otter.Cache[string, *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo]
	peerManagerState    otter.Cache[string, *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo]
	dataCompanionState  otter.Cache[string, *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo]
	cacheState          otter.Cache[string, *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo]
	logSyncState        otter.Cache[string, *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo]
	stateSyncState      otter.Cache[string, *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo]
	mempoolCacheState   otter.Cache[string, *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo]

	// info
	nodeInfo     otter.Cache[string, *v1.GetStatusResponse_NodeInfo]
	peers        otter.Cache[string, *v1.GetStatusResponse_PeerInfo]
	chainInfo    otter.Cache[string, *v1.GetStatusResponse_ChainInfo]
	syncInfo     otter.Cache[string, *v1.GetStatusResponse_SyncInfo]
	pruningInfo  otter.Cache[string, *v1.GetStatusResponse_PruningInfo]
	resourceInfo otter.Cache[string, *v1.GetStatusResponse_ResourceInfo]
	mempoolInfo  otter.Cache[string, *v1.GetStatusResponse_MempoolInfo]
	snapshotInfo otter.Cache[string, *v1.GetStatusResponse_SnapshotInfo]
}

func NewCache(config *config.Config) *Cache {
	c := &Cache{}
	c.currentHeight.Store(0)
	c.currentTxCount.Store(0)
	c.catchingUp.Store(true) // assume syncing on startup

	if err := c.initCaches(config); err != nil {
		panic(err)
	}

	return c
}

func initCache[T any](key string, initialValue T) otter.Cache[string, T] {
	builder := otter.MustBuilder[string, T](10_000)
	builder.Cost(func(key string, value T) uint32 {
		return 1
	})
	cache, err := builder.Build()
	if err != nil {
		panic(err)
	}
	set := cache.Set(key, initialValue)
	if !set {
		panic(fmt.Errorf("failed to set %s cache", key))
	}
	return cache
}

func upsertCache[T any](cache otter.Cache[string, T], key string, fn func(T) T) error {
	value, ok := cache.Get(key)
	if !ok {
		return fmt.Errorf("cache %s not found", key)
	}
	value = fn(value)
	set := cache.Set(key, value)
	if !set {
		return fmt.Errorf("failed to set %s cache", key)
	}
	return nil
}

func (c *Cache) initCaches(config *config.Config) error {
	// Initialize ABCI state cache
	c.abciState = initCache(ProcessStateABCI, &v1.GetStatusResponse_ProcessInfo_ProcessStateInfo{
		State:     v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_STARTING,
		StartedAt: timestamppb.New(time.Now()),
	})

	c.registryBridgeState = initCache(ProcessStateRegistryBridge, &v1.GetStatusResponse_ProcessInfo_ProcessStateInfo{
		State:     v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_STARTING,
		StartedAt: timestamppb.New(time.Now()),
	})

	c.echoServerState = initCache(ProcessStateEchoServer, &v1.GetStatusResponse_ProcessInfo_ProcessStateInfo{
		State:     v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_STARTING,
		StartedAt: timestamppb.New(time.Now()),
	})

	c.syncTasksState = initCache(ProcessStateSyncTasks, &v1.GetStatusResponse_ProcessInfo_ProcessStateInfo{
		State:     v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_STARTING,
		StartedAt: timestamppb.New(time.Now()),
	})

	c.peerManagerState = initCache(ProcessStatePeerManager, &v1.GetStatusResponse_ProcessInfo_ProcessStateInfo{
		State:     v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_STARTING,
		StartedAt: timestamppb.New(time.Now()),
	})

	c.dataCompanionState = initCache(ProcessStateDataCompanion, &v1.GetStatusResponse_ProcessInfo_ProcessStateInfo{
		State:     v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_STARTING,
		StartedAt: timestamppb.New(time.Now()),
	})

	c.cacheState = initCache(ProcessStateCache, &v1.GetStatusResponse_ProcessInfo_ProcessStateInfo{
		State:     v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_STARTING,
		StartedAt: timestamppb.New(time.Now()),
	})

	c.logSyncState = initCache(ProcessStateLogSync, &v1.GetStatusResponse_ProcessInfo_ProcessStateInfo{
		State:     v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_STARTING,
		StartedAt: timestamppb.New(time.Now()),
	})

	c.stateSyncState = initCache(ProcessStateStateSync, &v1.GetStatusResponse_ProcessInfo_ProcessStateInfo{
		State:     v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_STARTING,
		StartedAt: timestamppb.New(time.Now()),
	})

	c.mempoolCacheState = initCache(ProcessStateMempoolCache, &v1.GetStatusResponse_ProcessInfo_ProcessStateInfo{
		State:     v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_STARTING,
		StartedAt: timestamppb.New(time.Now()),
	})

	c.nodeInfo = initCache(NodeInfoKey, &v1.GetStatusResponse_NodeInfo{
		Endpoint:     config.NodeEndpoint,
		CometAddress: strings.ToLower(config.ProposerAddress),
		EthAddress:   strings.ToLower(config.WalletAddress),
		NodeType:     "validator",
	})

	c.peers = initCache(PeersKey, &v1.GetStatusResponse_PeerInfo{})

	c.chainInfo = initCache(ChainInfoKey, &v1.GetStatusResponse_ChainInfo{
		ChainId: config.GenesisFile.ChainID,
	})

	defaultSyncInfo := &v1.GetStatusResponse_SyncInfo{
		Synced: false,
	}
	c.syncInfo = initCache(SyncInfoKey, defaultSyncInfo)

	c.pruningInfo = initCache(PruningInfoKey, &v1.GetStatusResponse_PruningInfo{})

	c.resourceInfo = initCache(ResourceInfoKey, &v1.GetStatusResponse_ResourceInfo{})

	c.mempoolInfo = initCache(MempoolInfoKey, &v1.GetStatusResponse_MempoolInfo{})

	c.snapshotInfo = initCache(SnapshotInfoKey, &v1.GetStatusResponse_SnapshotInfo{})

	return nil
}

// maybe put a separate errgroup in here for things that
// continuously hydrate the cache
func (s *Server) startCache(ctx context.Context) error {
	s.logger.Info("caches initialized")

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.awaitRpcReady:
	}

	status, err := s.rpc.Status(ctx)
	if err != nil {
		return fmt.Errorf("could not get initial status: %v", err)
	}

	s.cache.currentHeight.Store(status.SyncInfo.LatestBlockHeight)

	cacheLifecycle := lifecycle.NewFromLifecycle(s.lc, "cache")
	cacheLifecycle.AddManagedRoutine("block event subscriber", s.startBlockEventSubscriber)
	cacheLifecycle.AddManagedRoutine("refresher", s.startCacheRefresh)
	cacheLifecycle.AddManagedRoutine("sync status refresher", s.refreshSyncStatus)

	<-ctx.Done()
	err = cacheLifecycle.ShutdownWithTimeout(15 * time.Second)
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (s *Server) startBlockEventSubscriber(ctx context.Context) error {
	node := s.node
	eb := node.EventBus()

	if eb == nil {
		return errors.New("event bus not ready")
	}

	subscriberID := "block-cache-subscriber"
	query := types.EventQueryNewBlock
	subscription, err := eb.Subscribe(ctx, subscriberID, query)
	if err != nil {
		return fmt.Errorf("failed to subscribe to NewBlock events: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Stopping block event subscription")
			return ctx.Err()
		case msg := <-subscription.Out():
			blockEvent := msg.Data().(types.EventDataNewBlock)
			blockHeight := blockEvent.Block.Height
			s.cache.currentHeight.Store(blockHeight)
			s.blockNumPubsub.Publish(ctx, BlockNumPubsubTopic, blockHeight)

			upsertCache(s.cache.chainInfo, ChainInfoKey, func(chainInfo *v1.GetStatusResponse_ChainInfo) *v1.GetStatusResponse_ChainInfo {
				chainInfo.CurrentHeight = blockHeight
				chainInfo.CurrentBlockHash = strings.ToLower(blockEvent.Block.Hash().String())
				return chainInfo
			})

		case <-subscription.Canceled():
			s.logger.Error("Subscription cancelled", zap.Error(subscription.Err()))
			return subscription.Err()
		}
	}
}

func (s *Server) startCacheRefresh(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)

	for {
		select {
		case <-ticker.C:
			wg := sync.WaitGroup{}
			wg.Add(2)
			go func() {
				defer wg.Done()
				if err := s.refreshResourceStatus(); err != nil {
					s.logger.Error("error refreshing resource status", zap.Error(err))
				}
			}()
			go func() {
				defer wg.Done()
				if err := s.cacheSnapshots(); err != nil {
					s.logger.Error("error caching snapshots", zap.Error(err))
				}
			}()
			wg.Wait()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Server) refreshSyncStatus(ctx context.Context) error {
	ticker := time.NewTicker(1 * time.Second)

	for {
		select {
		case <-ticker.C:
			if s.rpc == nil {
				return nil
			}

			status, err := s.rpc.Status(ctx)
			if err != nil {
				return fmt.Errorf("could not get status: %v", err)
			}

			upsertCache(s.cache.syncInfo, SyncInfoKey, func(syncInfo *v1.GetStatusResponse_SyncInfo) *v1.GetStatusResponse_SyncInfo {
				syncInfo.Synced = !status.SyncInfo.CatchingUp
				return syncInfo
			})
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Cache) UpdateProcessState(processKey string, state v1.GetStatusResponse_ProcessInfo_ProcessState, errorMsg string, metadata string) error {
	var processCache otter.Cache[string, *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo]

	switch processKey {
	case ProcessStateABCI:
		processCache = c.abciState
	case ProcessStateRegistryBridge:
		processCache = c.registryBridgeState
	case ProcessStateEchoServer:
		processCache = c.echoServerState
	case ProcessStateSyncTasks:
		processCache = c.syncTasksState
	case ProcessStatePeerManager:
		processCache = c.peerManagerState
	case ProcessStateDataCompanion:
		processCache = c.dataCompanionState
	case ProcessStateCache:
		processCache = c.cacheState
	case ProcessStateLogSync:
		processCache = c.logSyncState
	case ProcessStateStateSync:
		processCache = c.stateSyncState
	case ProcessStateMempoolCache:
		processCache = c.mempoolCacheState
	default:
		return fmt.Errorf("unknown process: %s", processKey)
	}

	return upsertCache(processCache, processKey, func(stateInfo *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo) *v1.GetStatusResponse_ProcessInfo_ProcessStateInfo {
		now := timestamppb.Now()
		stateInfo.State = state
		stateInfo.Error = errorMsg
		stateInfo.Metadata = metadata

		if state == v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_STARTING {
			stateInfo.StartedAt = now
			stateInfo.CompletedAt = nil
		} else if state == v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_COMPLETED || state == v1.GetStatusResponse_ProcessInfo_PROCESS_STATE_ERROR {
			stateInfo.CompletedAt = now
		}

		return stateInfo
	})
}
