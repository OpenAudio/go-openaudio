package explorer

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Cache is a generic thread-safe cache that periodically refreshes data in the background
type Cache[T any] struct {
	mu          sync.RWMutex
	data        T
	lastUpdated time.Time
	updateFunc  func(context.Context) (T, error)
	refreshRate time.Duration
	logger      *zap.Logger
}

// NewCache creates a new cache with the given update function and refresh rate
func NewCache[T any](updateFunc func(context.Context) (T, error), refreshRate time.Duration, logger *zap.Logger) *Cache[T] {
	return &Cache[T]{
		updateFunc:  updateFunc,
		refreshRate: refreshRate,
		logger:      logger,
	}
}

// Get returns the cached data (thread-safe read)
func (c *Cache[T]) Get() T {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data
}

// GetLastUpdated returns when the cache was last updated
func (c *Cache[T]) GetLastUpdated() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastUpdated
}

// StartRefresh begins the background refresh loop
// Should be called in a goroutine
func (c *Cache[T]) StartRefresh(ctx context.Context) {
	ticker := time.NewTicker(c.refreshRate)
	defer ticker.Stop()

	// Do initial refresh immediately (non-blocking)
	c.refresh(ctx)

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("Cache refresh stopped")
			return
		case <-ticker.C:
			c.refresh(ctx)
		}
	}
}

// refresh updates the cached data by calling the update function
func (c *Cache[T]) refresh(ctx context.Context) {
	start := time.Now()
	data, err := c.updateFunc(ctx)
	if err != nil {
		c.logger.Warn("Cache refresh failed", zap.Error(err), zap.Duration("duration", time.Since(start)))
		return
	}

	c.mu.Lock()
	c.data = data
	c.lastUpdated = time.Now()
	c.mu.Unlock()

	c.logger.Info("Cache refreshed", zap.Duration("duration", time.Since(start)))
}
