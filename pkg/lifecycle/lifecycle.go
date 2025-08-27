package lifecycle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AudiusProject/audiusd/pkg/common"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// Lifecycle formally manages various long-running goroutines
// for smooth cleanup when restarting major components (e.g. mediorum).
// This allows us to wait for all registered goroutines on a service to
// gracefully shut down before restarting the service.
type Lifecycle struct {
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	logger     *common.Logger
	z          *zap.Logger
	childrenMU sync.RWMutex
	children   []*Lifecycle
	isShutDown atomic.Bool
}

func NewLifecycle(ctx context.Context, name string, logger *common.Logger, z *zap.Logger) *Lifecycle {
	new_ctx, cancel := context.WithCancel(ctx)
	return &Lifecycle{
		ctx:      new_ctx,
		cancel:   cancel,
		logger:   logger.Child(name),
		z:        z,
		children: []*Lifecycle{},
	}
}

func NewFromLifecycle(lc *Lifecycle, z *zap.Logger, name string) *Lifecycle {
	if lc.isShutDown.Load() {
		panic("attempting to derive new lifecycle from already shut down lifecycle")
	}
	newLc := NewLifecycle(lc.ctx, name, lc.logger, z)
	lc.childrenMU.Lock()
	defer lc.childrenMU.Unlock()
	lc.children = append(lc.children, newLc)
	return newLc
}

func (l *Lifecycle) AddManagedRoutine(name string, f func(context.Context) error) {
	if l.isShutDown.Load() {
		panic("attempting to add managed routine to already shut down lifecycle")
	}
	l.logger.Info("starting managed routine", "routine", name)
	l.z.Info("starting managed routine", zap.String("routine", name))
	l.wg.Add(1)
	go func() {
		var err error
		defer l.logger.Info("managed routine was shut down", "routine", name, "error", err)
		defer func() {
			if err != nil {
				l.z.Info("managed routine was shut down", zap.String("routine", name), zap.Error(err))
			} else {
				l.z.Info("managed routine was shut down", zap.String("routine", name))
			}
		}()
		defer l.wg.Done()
		err = f(l.ctx)
	}()
}

func (l *Lifecycle) ShutdownWithTimeout(timeout time.Duration) error {
	l.cancel()
	l.isShutDown.Store(true)
	done := make(chan error, 1)

	eg := errgroup.Group{}

	for _, child := range l.children {
		eg.Go(func() error {
			return child.ShutdownWithTimeout(timeout)
		})
	}
	eg.Go(func() error {
		l.wg.Wait()
		return nil
	})

	go func() {
		done <- eg.Wait()
	}()

	l.logger.Info("Lifecycle shutdown signaled. Waiting for managed goroutines to finish...")
	l.z.Info("Lifecycle shutdown signaled. Waiting for managed goroutines to finish...")
	timeoutCh := time.After(timeout)
	select {
	case err := <-done:
		if err != nil {
			l.logger.Errorf("error shutting down child lifecycle: %v", err)
			l.z.Error("error shutting down child lifecycle", zap.Error(err))
		} else {
			l.logger.Info("Lifecycle shutdown complete")
			l.z.Info("Lifecycle shutdown complete")
		}
		return err
	case <-timeoutCh:
		l.logger.Info("Lifecycle shutdown timed out")
		l.z.Info("Lifecycle shutdown timed out")
		return errors.New("lifecycle shutdown timed out")
	}
}

func (l *Lifecycle) Wait() {
	l.wg.Wait()
}
