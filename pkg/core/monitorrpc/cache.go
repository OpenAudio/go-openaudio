// Package monitorrpc bounds repeated CometBFT status and validator queries.
package monitorrpc

import (
	"context"
	"fmt"
	"sync"
	"time"

	cmtjson "github.com/cometbft/cometbft/libs/json"
	"github.com/cometbft/cometbft/rpc/client"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"golang.org/x/sync/singleflight"
)

const ttl = 2 * time.Second
const maxEntries = 128

type entry struct {
	data    []byte
	expires time.Time
}

// Cache is shared by the local client, console and public RPC proxy. Only
// successful results are cached; callers receive independently decoded values.
type Cache struct {
	mu      sync.Mutex
	entries map[string]entry
	flights singleflight.Group
}

func New() *Cache { return &Cache{entries: make(map[string]entry)} }

func (c *Cache) Wrap(backend client.Client) *Client { return &Client{Client: backend, cache: c} }

type Client struct {
	client.Client
	cache *Cache
}

func (c *Cache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	return e.data, ok && time.Now().Before(e.expires)
}

func (c *Cache) load(ctx context.Context, key string, dst any, fetch func(context.Context) (any, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if data, ok := c.get(key); ok {
		return cmtjson.Unmarshal(data, dst)
	}
	done := c.flights.DoChan(key, func() (any, error) {
		if data, ok := c.get(key); ok {
			return data, nil
		}
		// One disconnected HTTP caller must not cancel work shared by other callers.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		value, err := fetch(loadCtx)
		if err != nil {
			return nil, err
		}
		data, err := cmtjson.Marshal(value)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		if len(c.entries) >= maxEntries {
			var oldest string
			var expiry time.Time
			for k, e := range c.entries {
				if oldest == "" || e.expires.Before(expiry) {
					oldest, expiry = k, e.expires
				}
			}
			delete(c.entries, oldest)
		}
		c.entries[key] = entry{data: data, expires: time.Now().Add(ttl)}
		c.mu.Unlock()
		return data, nil
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-done:
		if r.Err != nil {
			return r.Err
		}
		return cmtjson.Unmarshal(r.Val.([]byte), dst)
	}
}

func (c *Client) Status(ctx context.Context) (*ctypes.ResultStatus, error) {
	var result ctypes.ResultStatus
	err := c.cache.load(ctx, "status", &result, func(ctx context.Context) (any, error) { return c.Client.Status(ctx) })
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) Validators(ctx context.Context, height *int64, page, perPage *int) (*ctypes.ResultValidators, error) {
	// Resolve latest exactly as CometBFT does, then key every page by that height.
	var h int64
	if height != nil {
		h = *height
	}
	if height == nil {
		status, err := c.Status(ctx)
		if err != nil {
			return nil, err
		}
		h = status.SyncInfo.LatestBlockHeight
		if !status.SyncInfo.CatchingUp {
			h++
		}
	}
	p, n := 1, 30
	if page != nil {
		p = *page
	}
	if perPage != nil {
		n = *perPage
	}
	var result ctypes.ResultValidators
	err := c.cache.load(ctx, fmt.Sprintf("validators/%d/%d/%d", h, p, n), &result, func(ctx context.Context) (any, error) {
		return c.Client.Validators(ctx, &h, &p, &n)
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
