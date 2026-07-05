package fuzz

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Action interface {
	Name() string
	Run(ctx context.Context, run *RunContext) error
}

type ActionFunc struct {
	Label string
	Fn    func(context.Context, *RunContext) error
}

func (a ActionFunc) Name() string {
	if a.Label != "" {
		return a.Label
	}
	return "action"
}

func (a ActionFunc) Run(ctx context.Context, run *RunContext) error {
	if a.Fn == nil {
		return nil
	}
	return a.Fn(ctx, run)
}

func Wait(duration time.Duration) Action {
	return ActionFunc{
		Label: fmt.Sprintf("wait %s", duration),
		Fn: func(ctx context.Context, _ *RunContext) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
}

func StartNode(id NodeID) Action {
	return ActionFunc{
		Label: fmt.Sprintf("start %s", id),
		Fn: func(ctx context.Context, run *RunContext) error {
			return run.Network.StartNode(ctx, id)
		},
	}
}

func StopNode(id NodeID) Action {
	return ActionFunc{
		Label: fmt.Sprintf("stop %s", id),
		Fn: func(ctx context.Context, run *RunContext) error {
			return run.Network.StopNode(ctx, id)
		},
	}
}

func RestartNode(id NodeID) Action {
	return ActionFunc{
		Label: fmt.Sprintf("restart %s", id),
		Fn: func(ctx context.Context, run *RunContext) error {
			return run.Network.RestartNode(ctx, id)
		},
	}
}

func HookAction(name string, fn func(context.Context, *RunContext) error) Action {
	return ActionFunc{Label: name, Fn: fn}
}

func Sequence(name string, actions ...Action) Action {
	return ActionFunc{
		Label: name,
		Fn: func(ctx context.Context, run *RunContext) error {
			for _, action := range actions {
				if action == nil {
					continue
				}
				run.record("action_start", action.Name(), "")
				if err := action.Run(ctx, run); err != nil {
					return fmt.Errorf("%s: %w", action.Name(), err)
				}
				run.record("action_pass", action.Name(), "")
			}
			return nil
		},
	}
}

func Parallel(name string, actions ...Action) Action {
	return ActionFunc{
		Label: name,
		Fn: func(ctx context.Context, run *RunContext) error {
			var wg sync.WaitGroup
			errs := make(chan error, len(actions))
			for _, action := range actions {
				if action == nil {
					continue
				}
				action := action
				wg.Add(1)
				go func() {
					defer wg.Done()
					run.record("action_start", action.Name(), "")
					if err := action.Run(ctx, run); err != nil {
						errs <- fmt.Errorf("%s: %w", action.Name(), err)
						return
					}
					run.record("action_pass", action.Name(), "")
				}()
			}
			wg.Wait()
			close(errs)

			var joined []error
			for err := range errs {
				joined = append(joined, err)
			}
			return errors.Join(joined...)
		},
	}
}
