package server

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/cometbft/cometbft/types"
)

// a simple in memory cache of frequently queried things
// maybe upgrade to something like bigcache later
type Cache struct {
	currentHeight atomic.Int64
	catchingUp    atomic.Bool
}

func NewCache() *Cache {
	c := &Cache{}
	c.currentHeight.Store(0)
	c.catchingUp.Store(true) // assume syncing on startup
	return c
}

// maybe put a separate errgroup in here for things that
// continuously hydrate the cache
func (s *Server) startCache() error {
	<-s.awaitRpcReady

	status, err := s.rpc.Status(context.Background())
	if err != nil {
		return fmt.Errorf("could not get initial status: %v", err)
	}

	s.cache.currentHeight.Store(status.SyncInfo.LatestBlockHeight)

	node := s.node
	eb := node.EventBus()

	if eb == nil {
		return errors.New("event bus not ready")
	}

	subscriberID := "block-cache-subscriber"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	query := types.EventQueryNewBlock
	subscription, err := eb.Subscribe(ctx, subscriberID, query)
	if err != nil {
		return fmt.Errorf("failed to subscribe to NewBlock events: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Stopping block event subscription")
			return nil
		case msg := <-subscription.Out():
			blockEvent := msg.Data().(types.EventDataNewBlock)
			blockHeight := blockEvent.Block.Height
			s.cache.currentHeight.Store(blockHeight)
		case err := <-subscription.Canceled():
			s.logger.Errorf("Subscription cancelled: %v", err)
			return nil
		}
	}
}
