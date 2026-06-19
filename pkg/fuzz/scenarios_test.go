package fuzz

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type sequenceStatusReader struct {
	mu       sync.Mutex
	statuses map[NodeID][]NodeStatus
	calls    map[NodeID]int
}

func newSequenceStatusReader(statuses map[NodeID][]NodeStatus) *sequenceStatusReader {
	return &sequenceStatusReader{
		statuses: statuses,
		calls:    map[NodeID]int{},
	}
}

func (r *sequenceStatusReader) GetNodeStatus(_ context.Context, node NodeSpec) (NodeStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	statuses := r.statuses[node.ID]
	if len(statuses) == 0 {
		return NodeStatus{ID: node.ID, Endpoint: node.Endpoint, ObservedAt: time.Now().UTC()}, fmt.Errorf("missing statuses for %s", node.ID)
	}
	call := r.calls[node.ID]
	r.calls[node.ID]++
	if call >= len(statuses) {
		call = len(statuses) - 1
	}
	status := statuses[call]
	status.ID = node.ID
	status.Endpoint = node.Endpoint
	status.ObservedAt = time.Now().UTC()
	return status, nil
}

func liveStatus(height int64, hash string) NodeStatus {
	return NodeStatus{
		Reachable:      true,
		Ready:          true,
		Live:           true,
		Synced:         true,
		Height:         height,
		BlockHash:      hash,
		ValidatorPower: 10,
	}
}

func TestBasicLivenessScenarioPassesConvergedNetwork(t *testing.T) {
	network := newScenarioStatusNetwork(t, newSequenceStatusReader(map[NodeID][]NodeStatus{
		"node1": {liveStatus(10, "h10"), liveStatus(10, "h10"), liveStatus(11, "h11"), liveStatus(11, "h11")},
		"node2": {liveStatus(10, "h10"), liveStatus(10, "h10"), liveStatus(11, "h11"), liveStatus(11, "h11")},
	}))

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		BasicLivenessScenario(1, 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("basic liveness failed after %d events: %v", len(result.Events), err)
	}
}

func TestBasicLivenessScenarioCatchesLiveValidatorFork(t *testing.T) {
	network := newScenarioStatusNetwork(t, newSequenceStatusReader(map[NodeID][]NodeStatus{
		"node1": {liveStatus(10, "h10"), liveStatus(10, "h10"), liveStatus(11, "h11-a"), liveStatus(11, "h11-a")},
		"node2": {liveStatus(10, "h10"), liveStatus(10, "h10"), liveStatus(11, "h11-b"), liveStatus(11, "h11-b")},
	}))

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		BasicLivenessScenario(1, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected fork to fail basic liveness after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestLiveLivenessScenarioCatchesLaggingLiveValidator(t *testing.T) {
	network := newScenarioStatusNetwork(t, newSequenceStatusReader(map[NodeID][]NodeStatus{
		"node1": {liveStatus(10, "h10"), liveStatus(10, "h10"), liveStatus(11, "h11"), liveStatus(12, "h12"), liveStatus(13, "h13")},
		"node2": {liveStatus(8, "h8"), liveStatus(8, "h8"), liveStatus(8, "h8"), liveStatus(8, "h8"), liveStatus(8, "h8")},
	}))

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		LiveLivenessScenario(2, 1, 10*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected lagging validator to fail live liveness after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validator heights did not converge") {
		t.Fatalf("expected live-validator convergence failure, got %v", err)
	}
}

func newScenarioStatusNetwork(t *testing.T, reader StatusReader) *StaticNetwork {
	t.Helper()
	network, err := NewStaticNetwork(NetworkSpec{
		Name: "scenario-status",
		Nodes: []NodeSpec{
			{ID: "node1", Endpoint: "http://node1"},
			{ID: "node2", Endpoint: "http://node2"},
		},
	}, reader)
	if err != nil {
		t.Fatal(err)
	}
	return network
}
