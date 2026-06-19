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

func HeightStalls(observeFor, pollInterval time.Duration) Assertion {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if observeFor <= 0 {
		observeFor = 2 * pollInterval
	}
	return AssertionFunc{
		Label: "height does not advance",
		Fn: func(ctx context.Context, run *RunContext) error {
			initial, err := run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
			startHeight := initial.MaxHeight()

			deadline := time.NewTimer(observeFor)
			defer deadline.Stop()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			last := initial
			for {
				if last.MaxHeight() > startHeight {
					return fmt.Errorf("height advanced from %d to %d during %s stall observation: %s", startHeight, last.MaxHeight(), observeFor, last.Summary())
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-deadline.C:
					return nil
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

func HeightFollowsValidatorQuorum(within, pollInterval time.Duration) Assertion {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if within <= 0 {
		within = 2 * pollInterval
	}
	return AssertionFunc{
		Label: "height follows validator quorum",
		Fn: func(ctx context.Context, run *RunContext) error {
			snapshot, err := run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
			totalPower, livePower := snapshot.ValidatorPower()
			if livePower*3 > totalPower*2 {
				if err := observedHeightAdvances(ctx, run, snapshot, 1, within, pollInterval); err != nil {
					return fmt.Errorf("expected height to advance with live validator power %d/%d: %w", livePower, totalPower, err)
				}
				return nil
			}
			if err := observedHeightStalls(ctx, run, snapshot, within, pollInterval); err != nil {
				return fmt.Errorf("expected height to stall without validator quorum %d/%d: %w", livePower, totalPower, err)
			}
			return nil
		},
	}
}

func ValidatorOutcomeAssertions(within, pollInterval time.Duration, requireConvergence bool) []Assertion {
	regressionWindow := 2 * pollInterval
	if regressionWindow <= 0 {
		regressionWindow = 2 * defaultPollInterval
	}
	assertions := []Assertion{HeightFollowsValidatorQuorum(within, pollInterval)}
	if requireConvergence {
		assertions = append(assertions, LiveValidatorHeightsConverge(0, within, pollInterval))
	}
	assertions = append(assertions, NoLiveValidatorFork())
	assertions = append(assertions, NoHeightRegression(regressionWindow, pollInterval))
	return assertions
}

func NoLiveValidatorFork() Assertion {
	return AssertionFunc{
		Label: "no live validator fork",
		Fn: func(ctx context.Context, run *RunContext) error {
			snapshot, err := run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
			if conflicts := liveValidatorBlockHashConflicts(snapshot); len(conflicts) > 0 {
				return fmt.Errorf("live validators reported conflicting block hashes: %s", strings.Join(conflicts, "; "))
			}
			return nil
		},
	}
}

func liveValidatorBlockHashConflicts(snapshot Snapshot) []string {
	type blockSeen struct {
		node NodeID
		hash string
	}
	seen := map[int64]blockSeen{}
	var conflicts []string
	for _, node := range snapshot.Nodes {
		if !node.Reachable || !node.Live || node.ValidatorPower <= 0 || node.Height <= 0 || strings.TrimSpace(node.BlockHash) == "" {
			continue
		}
		previous, ok := seen[node.Height]
		if !ok {
			seen[node.Height] = blockSeen{node: node.ID, hash: node.BlockHash}
			continue
		}
		if previous.hash != node.BlockHash {
			conflicts = append(conflicts, fmt.Sprintf("height=%d %s=%s %s=%s", node.Height, previous.node, previous.hash, node.ID, node.BlockHash))
		}
	}
	return conflicts
}

func LiveValidatorHeightsConverge(maxSpread int64, within, pollInterval time.Duration) Assertion {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if within <= 0 {
		within = 2 * pollInterval
	}
	if maxSpread < 0 {
		maxSpread = 0
	}
	return AssertionFunc{
		Label: "live validator heights converge",
		Fn: func(ctx context.Context, run *RunContext) error {
			deadline := time.NewTimer(within)
			defer deadline.Stop()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			var last Snapshot
			var lastSpread int64
			var lastCount int
			for {
				snapshot, err := run.Network.Snapshot(ctx)
				if err != nil {
					return err
				}
				last = snapshot
				lastSpread, lastCount = liveValidatorHeightSpread(snapshot)
				if lastCount <= 1 || lastSpread <= maxSpread {
					return nil
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-deadline.C:
					return fmt.Errorf("live validator heights did not converge within %s: spread=%d max=%d validators=%d: %s", within, lastSpread, maxSpread, lastCount, last.Summary())
				case <-ticker.C:
				}
			}
		},
	}
}

func liveValidatorHeightSpread(snapshot Snapshot) (spread int64, count int) {
	var minHeight, maxHeight int64
	for _, node := range snapshot.Nodes {
		if !node.Reachable || !node.Live || node.ValidatorPower <= 0 || node.Height <= 0 {
			continue
		}
		if count == 0 || node.Height < minHeight {
			minHeight = node.Height
		}
		if count == 0 || node.Height > maxHeight {
			maxHeight = node.Height
		}
		count++
	}
	if count <= 1 {
		return 0, count
	}
	return maxHeight - minHeight, count
}

func observedHeightAdvances(ctx context.Context, run *RunContext, initial Snapshot, minDelta int64, within, pollInterval time.Duration) error {
	if minDelta <= 0 {
		minDelta = 1
	}
	startHeight := initial.MaxObservedHeight()
	target := startHeight + minDelta

	deadline := time.NewTimer(within)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	last := initial
	for {
		if last.MaxObservedHeight() >= target {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("observed height did not advance from %d to %d within %s: %s", startHeight, target, within, last.Summary())
		case <-ticker.C:
			var err error
			last, err = run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
		}
	}
}

func observedHeightStalls(ctx context.Context, run *RunContext, initial Snapshot, observeFor, pollInterval time.Duration) error {
	startHeight := initial.MaxObservedHeight()

	deadline := time.NewTimer(observeFor)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	last := initial
	for {
		if last.MaxObservedHeight() > startHeight {
			return fmt.Errorf("observed height advanced from %d to %d during %s stall observation: %s", startHeight, last.MaxObservedHeight(), observeFor, last.Summary())
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return nil
		case <-ticker.C:
			var err error
			last, err = run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
		}
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
