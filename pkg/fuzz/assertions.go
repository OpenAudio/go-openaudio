package fuzz

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const defaultPollInterval = time.Second

type Assertion interface {
	Name() string
	Check(ctx context.Context, run *RunContext) error
}

type AssertionFunc struct {
	Label string
	Fn    func(context.Context, *RunContext) error
}

func (a AssertionFunc) Name() string {
	if a.Label != "" {
		return a.Label
	}
	return "assertion"
}

func (a AssertionFunc) Check(ctx context.Context, run *RunContext) error {
	if a.Fn == nil {
		return nil
	}
	return a.Fn(ctx, run)
}

func AllReachable() Assertion {
	return AssertionFunc{
		Label: "all nodes reachable",
		Fn: func(ctx context.Context, run *RunContext) error {
			snapshot, err := run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
			var failed []string
			for _, node := range snapshot.Nodes {
				if !node.Reachable {
					failed = append(failed, fmt.Sprintf("%s: %s", node.ID, node.ObservationError))
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("unreachable nodes: %s", strings.Join(failed, "; "))
			}
			return nil
		},
	}
}

func ReachableAtLeast(required int, within, pollInterval time.Duration) Assertion {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	return AssertionFunc{
		Label: fmt.Sprintf("at least %d nodes reachable", required),
		Fn: func(ctx context.Context, run *RunContext) error {
			deadline := time.NewTimer(within)
			defer deadline.Stop()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			var last Snapshot
			for {
				snapshot, err := run.Network.Snapshot(ctx)
				if err != nil {
					return err
				}
				last = snapshot
				if snapshot.ReachableCount() >= required {
					return nil
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-deadline.C:
					return fmt.Errorf("reachable node count stayed below %d within %s: %s", required, within, last.Summary())
				case <-ticker.C:
				}
			}
		},
	}
}

func QuorumReady(required int, within, pollInterval time.Duration) Assertion {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	return AssertionFunc{
		Label: fmt.Sprintf("at least %d nodes ready", required),
		Fn: func(ctx context.Context, run *RunContext) error {
			deadline := time.NewTimer(within)
			defer deadline.Stop()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			var last Snapshot
			for {
				snapshot, err := run.Network.Snapshot(ctx)
				if err != nil {
					return err
				}
				last = snapshot
				if snapshot.ReadyCount() >= required {
					return nil
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-deadline.C:
					return fmt.Errorf("quorum not ready within %s: %s", within, last.Summary())
				case <-ticker.C:
				}
			}
		},
	}
}

func HeightAdvances(minDelta int64, within, pollInterval time.Duration) Assertion {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if minDelta <= 0 {
		minDelta = 1
	}
	return AssertionFunc{
		Label: fmt.Sprintf("height advances by %d", minDelta),
		Fn: func(ctx context.Context, run *RunContext) error {
			initial, err := run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
			startHeight := initial.MaxHeight()
			target := startHeight + minDelta

			deadline := time.NewTimer(within)
			defer deadline.Stop()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			last := initial
			for {
				if last.MaxHeight() >= target {
					return nil
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-deadline.C:
					return fmt.Errorf("height did not advance from %d to %d within %s: %s", startHeight, target, within, last.Summary())
				case <-ticker.C:
					last, err = run.Network.Snapshot(ctx)
					if err != nil {
						return err
					}
				}
			}
		},
	}
}

func NoHeightRegression(observeFor, pollInterval time.Duration) Assertion {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	return AssertionFunc{
		Label: "no height regression",
		Fn: func(ctx context.Context, run *RunContext) error {
			deadline := time.NewTimer(observeFor)
			defer deadline.Stop()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			seen := map[NodeID]int64{}
			for {
				snapshot, err := run.Network.Snapshot(ctx)
				if err != nil {
					return err
				}
				for _, node := range snapshot.Nodes {
					if !node.Reachable || node.Height == 0 {
						continue
					}
					if previous, ok := seen[node.ID]; ok && node.Height < previous {
						return fmt.Errorf("%s height regressed from %d to %d", node.ID, previous, node.Height)
					}
					seen[node.ID] = node.Height
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-deadline.C:
					return nil
				case <-ticker.C:
				}
			}
		},
	}
}

func ValidatorPowerEquals(id NodeID, power int64, within, pollInterval time.Duration) Assertion {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	return AssertionFunc{
		Label: fmt.Sprintf("%s validator power equals %d", id, power),
		Fn: func(ctx context.Context, run *RunContext) error {
			deadline := time.NewTimer(within)
			defer deadline.Stop()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			var last NodeStatus
			for {
				snapshot, err := run.Network.Snapshot(ctx)
				if err != nil {
					return err
				}
				node, ok := snapshot.ByNode(id)
				if !ok {
					return fmt.Errorf("%w: %s", ErrNodeNotFound, id)
				}
				last = node
				if node.Reachable && node.ValidatorPower == power {
					return nil
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-deadline.C:
					return fmt.Errorf("%s validator power did not become %d within %s; last=%d reachable=%t error=%s", id, power, within, last.ValidatorPower, last.Reachable, last.ObservationError)
				case <-ticker.C:
				}
			}
		},
	}
}
