package fuzz

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type scriptedReader struct {
	mu      sync.Mutex
	heights map[NodeID][]int64
	calls   map[NodeID]int
	ready   bool
}

func newScriptedReader(heights map[NodeID][]int64) *scriptedReader {
	return &scriptedReader{
		heights: heights,
		calls:   map[NodeID]int{},
		ready:   true,
	}
}

func (r *scriptedReader) GetNodeStatus(_ context.Context, node NodeSpec) (NodeStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	values := r.heights[node.ID]
	if len(values) == 0 {
		return NodeStatus{ID: node.ID, Endpoint: node.Endpoint, ObservedAt: time.Now().UTC()}, fmt.Errorf("no scripted heights for %s", node.ID)
	}
	call := r.calls[node.ID]
	r.calls[node.ID]++
	if call >= len(values) {
		call = len(values) - 1
	}
	return NodeStatus{
		ID:             node.ID,
		Endpoint:       node.Endpoint,
		Reachable:      true,
		Ready:          r.ready,
		Live:           true,
		Height:         values[call],
		ValidatorPower: 10,
		ObservedAt:     time.Now().UTC(),
	}, nil
}

type staticStatusReader map[NodeID]NodeStatus

func (r staticStatusReader) GetNodeStatus(_ context.Context, node NodeSpec) (NodeStatus, error) {
	status, ok := r[node.ID]
	if !ok {
		return NodeStatus{ID: node.ID, Endpoint: node.Endpoint, ObservedAt: time.Now().UTC()}, fmt.Errorf("missing status for %s", node.ID)
	}
	status.ID = node.ID
	status.Endpoint = node.Endpoint
	status.ObservedAt = time.Now().UTC()
	return status, nil
}

func TestHeightAdvances(t *testing.T) {
	reader := newScriptedReader(map[NodeID][]int64{
		"node1": {10, 10, 11},
		"node2": {10, 11, 12},
	})
	network, err := NewStaticNetwork(NetworkSpec{
		Name: "scripted",
		Nodes: []NodeSpec{
			{ID: "node1", Endpoint: "http://node1"},
			{ID: "node2", Endpoint: "http://node2"},
		},
	}, reader)
	if err != nil {
		t.Fatal(err)
	}

	run := &RunContext{Network: network}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := HeightAdvances(2, time.Second, time.Millisecond).Check(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func TestHeightStalls(t *testing.T) {
	reader := newScriptedReader(map[NodeID][]int64{
		"node1": {10, 10, 10},
		"node2": {10, 10, 10},
	})
	network, err := NewStaticNetwork(NetworkSpec{
		Name: "scripted",
		Nodes: []NodeSpec{
			{ID: "node1", Endpoint: "http://node1"},
			{ID: "node2", Endpoint: "http://node2"},
		},
	}, reader)
	if err != nil {
		t.Fatal(err)
	}

	run := &RunContext{Network: network}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := HeightStalls(10*time.Millisecond, time.Millisecond).Check(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func TestHeightStallsFails(t *testing.T) {
	reader := newScriptedReader(map[NodeID][]int64{
		"node1": {10, 11},
		"node2": {10, 10},
	})
	network, err := NewStaticNetwork(NetworkSpec{
		Name: "scripted",
		Nodes: []NodeSpec{
			{ID: "node1", Endpoint: "http://node1"},
			{ID: "node2", Endpoint: "http://node2"},
		},
	}, reader)
	if err != nil {
		t.Fatal(err)
	}

	run := &RunContext{Network: network}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := HeightStalls(10*time.Millisecond, time.Millisecond).Check(ctx, run); err == nil {
		t.Fatal("expected height stall assertion to fail")
	}
}

func TestReachableAtLeast(t *testing.T) {
	network, err := NewStaticNetwork(NetworkSpec{
		Name: "reachable",
		Nodes: []NodeSpec{
			{ID: "node1", Endpoint: "http://node1"},
			{ID: "node2", Endpoint: "http://node2"},
			{ID: "node3", Endpoint: "http://node3"},
		},
	}, staticStatusReader{
		"node1": {Reachable: true},
		"node2": {Reachable: true},
		"node3": {Reachable: false, ObservationError: "down"},
	})
	if err != nil {
		t.Fatal(err)
	}

	run := &RunContext{Network: network}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := ReachableAtLeast(2, time.Second, time.Millisecond).Check(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := ReachableAtLeast(3, 5*time.Millisecond, time.Millisecond).Check(ctx, run); err == nil {
		t.Fatal("expected reachable quorum assertion to fail")
	}
}

func TestHeightFollowsValidatorQuorumAdvances(t *testing.T) {
	reader := newScriptedReader(map[NodeID][]int64{
		"node1": {10, 10, 11},
		"node2": {10, 11, 12},
		"node3": {10, 11, 12},
	})
	network, err := NewStaticNetwork(NetworkSpec{
		Name: "scripted",
		Nodes: []NodeSpec{
			{ID: "node1", Endpoint: "http://node1"},
			{ID: "node2", Endpoint: "http://node2"},
			{ID: "node3", Endpoint: "http://node3"},
		},
	}, reader)
	if err != nil {
		t.Fatal(err)
	}

	run := &RunContext{Network: network}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := HeightFollowsValidatorQuorum(time.Second, time.Millisecond).Check(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func TestHeightFollowsValidatorQuorumStalls(t *testing.T) {
	network, err := NewStaticNetwork(NetworkSpec{
		Name: "scripted",
		Nodes: []NodeSpec{
			{ID: "node1", Endpoint: "http://node1"},
			{ID: "node2", Endpoint: "http://node2"},
			{ID: "node3", Endpoint: "http://node3"},
		},
	}, staticStatusReader{
		"node1": {Reachable: true, Live: true, Height: 10, ValidatorPower: 10},
		"node2": {Reachable: true, Live: false, Height: 10, ValidatorPower: 10},
		"node3": {Reachable: true, Live: false, Height: 10, ValidatorPower: 10},
	})
	if err != nil {
		t.Fatal(err)
	}

	run := &RunContext{Network: network}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := HeightFollowsValidatorQuorum(10*time.Millisecond, time.Millisecond).Check(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func TestHeightFollowsValidatorQuorumStallsWithNoValidatorPower(t *testing.T) {
	network, err := NewStaticNetwork(NetworkSpec{
		Name:  "scripted",
		Nodes: []NodeSpec{{ID: "node1", Endpoint: "http://node1"}},
	}, staticStatusReader{
		"node1": {Reachable: false, Live: false, Height: 10, ValidatorPower: 0},
	})
	if err != nil {
		t.Fatal(err)
	}

	run := &RunContext{Network: network}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := HeightFollowsValidatorQuorum(10*time.Millisecond, time.Millisecond).Check(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func TestLiveValidatorHeightsConverge(t *testing.T) {
	reader := newScriptedReader(map[NodeID][]int64{
		"node1": {10, 11, 12},
		"node2": {8, 10, 12},
		"node3": {9, 11, 12},
	})
	network, err := NewStaticNetwork(NetworkSpec{
		Name: "scripted",
		Nodes: []NodeSpec{
			{ID: "node1", Endpoint: "http://node1"},
			{ID: "node2", Endpoint: "http://node2"},
			{ID: "node3", Endpoint: "http://node3"},
		},
	}, reader)
	if err != nil {
		t.Fatal(err)
	}

	run := &RunContext{Network: network}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := LiveValidatorHeightsConverge(0, time.Second, time.Millisecond).Check(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func TestLiveValidatorHeightsConvergeFails(t *testing.T) {
	reader := newScriptedReader(map[NodeID][]int64{
		"node1": {10, 11, 12, 13},
		"node2": {8, 8, 8, 8},
		"node3": {9, 10, 11, 12},
	})
	network, err := NewStaticNetwork(NetworkSpec{
		Name: "scripted",
		Nodes: []NodeSpec{
			{ID: "node1", Endpoint: "http://node1"},
			{ID: "node2", Endpoint: "http://node2"},
			{ID: "node3", Endpoint: "http://node3"},
		},
	}, reader)
	if err != nil {
		t.Fatal(err)
	}

	run := &RunContext{Network: network}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := LiveValidatorHeightsConverge(0, 10*time.Millisecond, time.Millisecond).Check(ctx, run); err == nil {
		t.Fatal("expected convergence assertion to fail")
	}
}

func TestNoHeightRegressionFails(t *testing.T) {
	reader := newScriptedReader(map[NodeID][]int64{
		"node1": {10, 9},
	})
	network, err := NewStaticNetwork(NetworkSpec{
		Name:  "scripted",
		Nodes: []NodeSpec{{ID: "node1", Endpoint: "http://node1"}},
	}, reader)
	if err != nil {
		t.Fatal(err)
	}

	run := &RunContext{Network: network}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := NoHeightRegression(25*time.Millisecond, time.Millisecond).Check(ctx, run); err == nil {
		t.Fatal("expected regression assertion to fail")
	}
}
