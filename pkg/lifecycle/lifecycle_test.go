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
)

func TestLifecycleShutsDown(t *testing.T) {
	ctx := context.Background()
	lc := NewLifecycle(ctx, "test lifecycle", common.NewLogger(&slog.HandlerOptions{}))

	// Add routines and children asynchronously to test thread safety
	var childLc1 *Lifecycle
	var childLc2 *Lifecycle
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		childLc1 = NewFromLifecycle(lc, "child lc 1")
		wg.Done()
	}()
	go func() {
		childLc2 = NewFromLifecycle(lc, "child lc 2")
		wg.Done()
	}()

	wg.Wait()

	wg.Add(9)
	go func() {
		lc.AddManagedRoutine("doAThing 1", doAThing)
		wg.Done()
	}()
	go func() {
		lc.AddManagedRoutine("doAThing 2", doAThing)
		wg.Done()
	}()
	go func() {
		lc.AddManagedRoutine("doAThing 3", doAThing)
		wg.Done()
	}()

	go func() {
		childLc1.AddManagedRoutine("doAThing c1 1", doAThing)
		wg.Done()
	}()
	go func() {
		childLc1.AddManagedRoutine("doAThing c1 2", doAThing)
		wg.Done()
	}()
	go func() {
		childLc1.AddManagedRoutine("doAThing c1 3", doAThing)
		wg.Done()
	}()

	go func() {
		childLc2.AddManagedRoutine("doAThing c2 1", doAThing)
		wg.Done()
	}()
	go func() {
		childLc2.AddManagedRoutine("doAThing c2 2", doAThing)
		wg.Done()
	}()
	go func() {
		childLc2.AddManagedRoutine("doAThing c2 3", doAThing)
		wg.Done()
	}()

	wg.Wait()

	err := childLc2.ShutdownWithTimeout(3 * time.Second)
	assert.NoError(t, err)
	assert.Panics(t, func() { childLc2.AddManagedRoutine("should panic", doAThing) })

	err = lc.ShutdownWithTimeout(3 * time.Second)
	assert.NoError(t, err)
	assert.Panics(t, func() { lc.AddManagedRoutine("should panic", doAThing) })
	assert.Panics(t, func() { childLc1.AddManagedRoutine("should panic", doAThing) })
}

func TestLifecycleTimesOut(t *testing.T) {
	ctx := context.Background()
	lc := NewLifecycle(ctx, "test lifecycle", common.NewLogger(&slog.HandlerOptions{}))
	lc.AddManagedRoutine("doAThingForever", doAThingForever)
	err := lc.ShutdownWithTimeout(3 * time.Second)
	assert.Error(t, err)
}

func doAThing(ctx context.Context) {
	<-ctx.Done()
	time.Sleep(time.Duration(rand.Intn(3)) * time.Second)
}

func doAThingForever(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	for {
		select {
		case <-ticker.C:
			time.Sleep(2 * time.Second)
		}
	}
}
