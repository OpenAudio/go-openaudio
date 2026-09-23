package server

import (
	"context"
	"testing"
	"time"

	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBlockEventSubscriberRecoversAfterCancellation(t *testing.T) {
	eb := types.NewEventBus()
	require.NoError(t, eb.Start())
	defer eb.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{logger: zap.NewNop()}
	done := make(chan error, 1)
	go func() { done <- s.startBlockEventSubscriber(ctx, eb) }()

	// Cancel the real Comet subscription twice; the updater must reattach each time.
	for i := 0; i < 2; i++ {
		require.Eventually(t, func() bool {
			return eb.NumClientSubscriptions("block-cache-subscriber") == 1
		}, 5*time.Second, 10*time.Millisecond)
		require.NoError(t, eb.Unsubscribe(ctx, "block-cache-subscriber", types.EventQueryNewBlock))
		require.Eventually(t, func() bool {
			return eb.NumClientSubscriptions("block-cache-subscriber") == 0
		}, time.Second, time.Millisecond)
	}
	require.Eventually(t, func() bool {
		return eb.NumClientSubscriptions("block-cache-subscriber") == 1
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("subscriber did not stop on shutdown")
	}
	require.Zero(t, eb.NumClientSubscriptions("block-cache-subscriber"))
}
