package fuzz

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type validatorPowerBaselineNode struct {
	power int64
	live  bool
}

type reachabilityBaselineNode struct {
	endpoint  string
	reachable bool
	ready     bool
}

type ValidatorPowerBaseline struct {
	mu         sync.Mutex
	captured   bool
	totalPower int64
	livePower  int64
	nodes      map[NodeID]validatorPowerBaselineNode
	summary    string
}

type ReachabilityBaseline struct {
	mu       sync.Mutex
	captured bool
	nodes    map[NodeID]reachabilityBaselineNode
	summary  string
}

func CaptureValidatorPowerBaseline(baseline *ValidatorPowerBaseline) Action {
	return ActionFunc{
		Label: "capture validator power baseline",
		Fn: func(ctx context.Context, run *RunContext) error {
			if baseline == nil {
				return fmt.Errorf("%w: validator power baseline is nil", ErrInvalidScenario)
			}
			snapshot, err := run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
			totalPower, livePower := snapshot.ValidatorPower()
			if totalPower <= 0 {
				return fmt.Errorf("%w: snapshot has no validator power: %s", ErrInvalidScenario, snapshot.Summary())
			}
			baseline.capture(totalPower, livePower, validatorPowerBaselineNodes(snapshot), snapshot.Summary())
			run.record("validator_power_baseline", fmt.Sprintf("live=%d total=%d", livePower, totalPower), snapshot.Summary())
			return nil
		},
	}
}

func CaptureReachabilityBaseline(baseline *ReachabilityBaseline) Action {
	return ActionFunc{
		Label: "capture reachability baseline",
		Fn: func(ctx context.Context, run *RunContext) error {
			if baseline == nil {
				return fmt.Errorf("%w: reachability baseline is nil", ErrInvalidScenario)
			}
			snapshot, err := run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
			baseline.capture(reachabilityBaselineNodes(snapshot), snapshot.Summary())
			run.record("reachability_baseline", fmt.Sprintf("nodes=%d", len(snapshot.Nodes)), snapshot.Summary())
			return nil
		},
	}
}

func ValidatorPowerRestored(baseline *ValidatorPowerBaseline, within, pollInterval time.Duration) Assertion {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if within <= 0 {
		within = 2 * pollInterval
	}
	return AssertionFunc{
		Label: "validator power restored to baseline",
		Fn: func(ctx context.Context, run *RunContext) error {
			wantTotal, wantLive, wantNodes, baselineSummary, ok := baseline.values()
			if !ok {
				return fmt.Errorf("%w: validator power baseline was not captured", ErrInvalidScenario)
			}

			deadline := time.NewTimer(within)
			defer deadline.Stop()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			var last Snapshot
			var gotTotal, gotLive int64
			var mismatches []string
			for {
				snapshot, err := run.Network.Snapshot(ctx)
				if err != nil {
					return err
				}
				last = snapshot
				gotTotal, gotLive = snapshot.ValidatorPower()
				mismatches = validatorPowerBaselineMismatches(snapshot, wantNodes)
				if gotTotal == wantTotal && gotLive == wantLive && len(mismatches) == 0 {
					return nil
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-deadline.C:
					return fmt.Errorf(
						"validator power did not return to baseline within %s: got live=%d total=%d want live=%d total=%d mismatches=%s baseline=%s last=%s",
						within,
						gotLive,
						gotTotal,
						wantLive,
						wantTotal,
						formatMismatches(mismatches),
						baselineSummary,
						last.Summary(),
					)
				case <-ticker.C:
				}
			}
		},
	}
}

func ReachabilityRestored(baseline *ReachabilityBaseline, within, pollInterval time.Duration) Assertion {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if within <= 0 {
		within = 2 * pollInterval
	}
	return AssertionFunc{
		Label: "reachability restored to baseline",
		Fn: func(ctx context.Context, run *RunContext) error {
			wantNodes, baselineSummary, ok := baseline.values()
			if !ok {
				return fmt.Errorf("%w: reachability baseline was not captured", ErrInvalidScenario)
			}

			deadline := time.NewTimer(within)
			defer deadline.Stop()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			var last Snapshot
			var mismatches []string
			for {
				snapshot, err := run.Network.Snapshot(ctx)
				if err != nil {
					return err
				}
				last = snapshot
				mismatches = reachabilityBaselineMismatches(snapshot, wantNodes)
				if len(mismatches) == 0 {
					return nil
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-deadline.C:
					return fmt.Errorf(
						"reachability did not return to baseline within %s: mismatches=%s baseline=%s last=%s",
						within,
						formatMismatches(mismatches),
						baselineSummary,
						last.Summary(),
					)
				case <-ticker.C:
				}
			}
		},
	}
}

func (b *ValidatorPowerBaseline) capture(totalPower, livePower int64, nodes map[NodeID]validatorPowerBaselineNode, summary string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.captured = true
	b.totalPower = totalPower
	b.livePower = livePower
	b.nodes = nodes
	b.summary = summary
}

func (b *ValidatorPowerBaseline) values() (totalPower, livePower int64, nodes map[NodeID]validatorPowerBaselineNode, summary string, ok bool) {
	if b == nil {
		return 0, 0, nil, "", false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	nodes = make(map[NodeID]validatorPowerBaselineNode, len(b.nodes))
	for id, node := range b.nodes {
		nodes[id] = node
	}
	return b.totalPower, b.livePower, nodes, b.summary, b.captured
}

func (b *ReachabilityBaseline) capture(nodes map[NodeID]reachabilityBaselineNode, summary string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.captured = true
	b.nodes = nodes
	b.summary = summary
}

func (b *ReachabilityBaseline) values() (nodes map[NodeID]reachabilityBaselineNode, summary string, ok bool) {
	if b == nil {
		return nil, "", false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	nodes = make(map[NodeID]reachabilityBaselineNode, len(b.nodes))
	for id, node := range b.nodes {
		nodes[id] = node
	}
	return nodes, b.summary, b.captured
}

func validatorPowerBaselineNodes(snapshot Snapshot) map[NodeID]validatorPowerBaselineNode {
	nodes := make(map[NodeID]validatorPowerBaselineNode, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		nodes[node.ID] = validatorPowerBaselineNode{
			power: node.ValidatorPower,
			live:  node.Live,
		}
	}
	return nodes
}

func reachabilityBaselineNodes(snapshot Snapshot) map[NodeID]reachabilityBaselineNode {
	nodes := make(map[NodeID]reachabilityBaselineNode, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		nodes[node.ID] = reachabilityBaselineNode{
			endpoint:  node.Endpoint,
			reachable: node.Reachable,
			ready:     node.Ready,
		}
	}
	return nodes
}

func validatorPowerBaselineMismatches(snapshot Snapshot, want map[NodeID]validatorPowerBaselineNode) []string {
	seen := make(map[NodeID]struct{}, len(snapshot.Nodes))
	var mismatches []string
	for _, node := range snapshot.Nodes {
		seen[node.ID] = struct{}{}
		wantNode, ok := want[node.ID]
		if !ok {
			if node.ValidatorPower > 0 || node.Live {
				mismatches = append(mismatches, fmt.Sprintf("%s extra live=%t power=%d", node.ID, node.Live, node.ValidatorPower))
			}
			continue
		}
		if node.ValidatorPower != wantNode.power || node.Live != wantNode.live {
			mismatches = append(mismatches, fmt.Sprintf("%s live=%t power=%d want live=%t power=%d", node.ID, node.Live, node.ValidatorPower, wantNode.live, wantNode.power))
		}
	}
	for id, wantNode := range want {
		if _, ok := seen[id]; !ok {
			mismatches = append(mismatches, fmt.Sprintf("%s missing want live=%t power=%d", id, wantNode.live, wantNode.power))
		}
	}
	sort.Strings(mismatches)
	return mismatches
}

func reachabilityBaselineMismatches(snapshot Snapshot, want map[NodeID]reachabilityBaselineNode) []string {
	seen := make(map[NodeID]struct{}, len(snapshot.Nodes))
	var mismatches []string
	for _, node := range snapshot.Nodes {
		seen[node.ID] = struct{}{}
		wantNode, ok := want[node.ID]
		if !ok {
			if node.Reachable || node.Ready {
				mismatches = append(mismatches, fmt.Sprintf("%s extra reachable=%t ready=%t endpoint=%s", node.ID, node.Reachable, node.Ready, node.Endpoint))
			}
			continue
		}
		if node.Endpoint != wantNode.endpoint || node.Reachable != wantNode.reachable || node.Ready != wantNode.ready {
			mismatches = append(mismatches, fmt.Sprintf(
				"%s reachable=%t ready=%t endpoint=%s want reachable=%t ready=%t endpoint=%s",
				node.ID,
				node.Reachable,
				node.Ready,
				node.Endpoint,
				wantNode.reachable,
				wantNode.ready,
				wantNode.endpoint,
			))
		}
	}
	for id, wantNode := range want {
		if _, ok := seen[id]; !ok {
			mismatches = append(mismatches, fmt.Sprintf("%s missing want reachable=%t ready=%t endpoint=%s", id, wantNode.reachable, wantNode.ready, wantNode.endpoint))
		}
	}
	sort.Strings(mismatches)
	return mismatches
}

func formatMismatches(mismatches []string) string {
	if len(mismatches) == 0 {
		return "none"
	}
	const limit = 5
	if len(mismatches) <= limit {
		return fmt.Sprintf("%q", mismatches)
	}
	return fmt.Sprintf("%q and %d more", mismatches[:limit], len(mismatches)-limit)
}
