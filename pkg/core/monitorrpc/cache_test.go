package monitorrpc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/rpc/client"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

type fakeClient struct {
	client.Client
	status     func(context.Context) (*ctypes.ResultStatus, error)
	validators func(context.Context, *int64, *int, *int) (*ctypes.ResultValidators, error)
}

func (f *fakeClient) Status(ctx context.Context) (*ctypes.ResultStatus, error) { return f.status(ctx) }
func (f *fakeClient) Validators(ctx context.Context, h *int64, p, n *int) (*ctypes.ResultValidators, error) {
	return f.validators(ctx, h, p, n)
}

func expire(c *Cache) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		e.expires = time.Time{}
		c.entries[k] = e
	}
}

func TestSharedStatusCacheCoalescesAndCopies(t *testing.T) {
	cache := New()
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	backend := &fakeClient{status: func(context.Context) (*ctypes.ResultStatus, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return &ctypes.ResultStatus{SyncInfo: ctypes.SyncInfo{LatestBlockHeight: 42}}, nil
	}}
	a, b := cache.Wrap(backend), cache.Wrap(backend)
	var wg sync.WaitGroup
	results := make([]*ctypes.ResultStatus, 100)
	errs := make([]error, 100)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := a
			if i%2 == 1 {
				c = b
			}
			results[i], errs[i] = c.Status(context.Background())
		}(i)
	}
	<-started
	close(release)
	wg.Wait()
	for i := range results {
		require.NoError(t, errs[i])
		require.EqualValues(t, 42, results[i].SyncInfo.LatestBlockHeight)
	}
	require.EqualValues(t, 1, calls.Load())
	results[0].SyncInfo.LatestBlockHeight = 900
	again, err := b.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 42, again.SyncInfo.LatestBlockHeight)
	expire(cache)
	_, err = a.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
}

func TestCanceledCallerDoesNotCancelSharedLoad(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var loadCtx context.Context
	c := New().Wrap(&fakeClient{status: func(ctx context.Context) (*ctypes.ResultStatus, error) {
		loadCtx = ctx
		close(started)
		<-release
		return &ctypes.ResultStatus{}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Status(ctx); done <- err }()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.NoError(t, loadCtx.Err())
	close(release)
	_, err := c.Status(context.Background())
	require.NoError(t, err)
}

func TestFailuresAreRetried(t *testing.T) {
	var calls int
	c := New().Wrap(&fakeClient{status: func(context.Context) (*ctypes.ResultStatus, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("unavailable")
		}
		return &ctypes.ResultStatus{}, nil
	}})
	_, err := c.Status(context.Background())
	require.Error(t, err)
	_, err = c.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}

func TestValidatorHeightAndPaginationKeys(t *testing.T) {
	cache := New()
	height := int64(100)
	syncing := false
	var calls []string
	c := cache.Wrap(&fakeClient{
		status: func(context.Context) (*ctypes.ResultStatus, error) {
			return &ctypes.ResultStatus{SyncInfo: ctypes.SyncInfo{LatestBlockHeight: height, CatchingUp: syncing}}, nil
		},
		validators: func(_ context.Context, h *int64, p, n *int) (*ctypes.ResultValidators, error) {
			calls = append(calls, fmt.Sprintf("%d/%d/%d", *h, *p, *n))
			return &ctypes.ResultValidators{BlockHeight: *h}, nil
		},
	})
	ctx := context.Background()
	p, n := 2, 50
	historical := int64(50)
	for range 2 {
		r, err := c.Validators(ctx, nil, nil, nil)
		require.NoError(t, err)
		require.EqualValues(t, 101, r.BlockHeight)
	}
	_, err := c.Validators(ctx, nil, &p, &n)
	require.NoError(t, err)
	_, err = c.Validators(ctx, &historical, &p, &n)
	require.NoError(t, err)
	require.Equal(t, []string{"101/1/30", "101/2/50", "50/2/50"}, calls)
	zero := int64(0)
	_, err = c.Validators(ctx, &zero, nil, nil)
	require.NoError(t, err) // fake records it; the real backend rejects explicit zero
	require.Equal(t, "0/1/30", calls[len(calls)-1])
	expire(cache)
	height = 102
	syncing = true
	r, err := c.Validators(ctx, nil, nil, nil)
	require.NoError(t, err)
	require.EqualValues(t, 102, r.BlockHeight)
}

func TestCacheIsBounded(t *testing.T) {
	c := New()
	for i := 0; i < maxEntries+20; i++ {
		var dst string
		require.NoError(t, c.load(context.Background(), fmt.Sprint(i), &dst, func(context.Context) (any, error) { return "ok", nil }))
	}
	require.Len(t, c.entries, maxEntries)
}

// Reproduce LoadValidators' expensive proposer-priority reconstruction near
// the end of a checkpoint interval. Run with -bench Status -benchtime=20x.
func BenchmarkStatusValidatorReconstruction(b *testing.B) {
	vals := make([]*types.Validator, 69)
	for i := range vals {
		vals[i] = types.NewValidator(ed25519.GenPrivKeyFromSecret([]byte(fmt.Sprint(i))).PubKey(), 10)
	}
	set := types.NewValidatorSet(vals)
	for _, cached := range []bool{false, true} {
		b.Run(fmt.Sprintf("cached=%v", cached), func(b *testing.B) {
			var calls atomic.Int64
			var backend client.Client = &fakeClient{status: func(context.Context) (*ctypes.ResultStatus, error) {
				calls.Add(1)
				v := set.Copy()
				v.IncrementProposerPriority(54890)
				return &ctypes.ResultStatus{ValidatorInfo: ctypes.ValidatorInfo{PubKey: v.Validators[0].PubKey}}, nil
			}}
			if cached {
				backend = New().Wrap(backend)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := backend.Status(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(calls.Load()), "reconstructions")
		})
	}
}
