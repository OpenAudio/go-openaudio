package lifecycle

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestLifecycleShutsDown(t *testing.T) {
	ctx := context.Background()
	lc := NewLifecycle(ctx, "test lifecycle", common.NewLogger(&slog.HandlerOptions{}), zap.NewNop())

	// Add routines and children asynchronously to test thread safety
	var childLc1 *Lifecycle
	var childLc2 *Lifecycle
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		childLc1 = NewFromLifecycle(lc, zap.NewNop(), "child lc 1")
		wg.Done()
	}()
	go func() {
		childLc2 = NewFromLifecycle(lc, zap.NewNop(), "child lc 2")
		wg.Done()
	}()

	wg.Wait()

	wg.Add(9)
	go func() {
		lc.AddManagedRoutine("dummyRoutine 1", dummyRoutine)
		wg.Done()
	}()
	go func() {
		lc.AddManagedRoutine("dummyRoutine 2", dummyRoutine)
		wg.Done()
	}()
	go func() {
		lc.AddManagedRoutine("dummyRoutine 3", dummyRoutine)
		wg.Done()
	}()

	go func() {
		childLc1.AddManagedRoutine("dummyRoutine c1 1", dummyRoutine)
		wg.Done()
	}()
	go func() {
		childLc1.AddManagedRoutine("dummyRoutine c1 2", dummyRoutine)
		wg.Done()
	}()
	go func() {
		childLc1.AddManagedRoutine("dummyRoutine c1 3", dummyRoutine)
		wg.Done()
	}()

	go func() {
		childLc2.AddManagedRoutine("dummyRoutine c2 1", dummyRoutine)
		wg.Done()
	}()
	go func() {
		childLc2.AddManagedRoutine("dummyRoutine c2 2", dummyRoutine)
		wg.Done()
	}()
	go func() {
		childLc2.AddManagedRoutine("dummyRoutine c2 3", dummyRoutine)
		wg.Done()
	}()

	wg.Wait()

	err := childLc2.ShutdownWithTimeout(3 * time.Second)
	assert.NoError(t, err)
	assert.Panics(t, func() { childLc2.AddManagedRoutine("should panic", dummyRoutine) })

	err = lc.ShutdownWithTimeout(3 * time.Second)
	assert.NoError(t, err)
	assert.Panics(t, func() { lc.AddManagedRoutine("should panic", dummyRoutine) })
	assert.Panics(t, func() { childLc1.AddManagedRoutine("should panic", dummyRoutine) })
}

func TestLifecycleTimesOut(t *testing.T) {
	ctx := context.Background()
	lc := NewLifecycle(ctx, "test lifecycle", common.NewLogger(&slog.HandlerOptions{}), zap.NewNop())
	lc.AddManagedRoutine("dummyRoutineThatNeverEnds", dummyRoutineThatNeverEnds)
	err := lc.ShutdownWithTimeout(3 * time.Second)
	assert.Error(t, err)
}

func dummyRoutine(ctx context.Context) error {
	<-ctx.Done()
	time.Sleep(time.Duration(rand.Intn(3)) * time.Second)
	return nil
}

func dummyRoutineThatNeverEnds(ctx context.Context) error {
	ticker := time.NewTicker(1 * time.Minute)
	for {
		select {
		case <-ticker.C:
			time.Sleep(2 * time.Second)
		}
	}
}
