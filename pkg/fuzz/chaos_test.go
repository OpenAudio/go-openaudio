package fuzz

import (
	"context"
	"math/rand"
	"sync"
	"testing"
	"time"
)

type recordingNetwork struct {
	mu       sync.Mutex
	spec     NetworkSpec
	reader   StatusReader
	started  []NodeID
	stopped  []NodeID
	restarts []NodeID
}

func (n *recordingNetwork) Spec() NetworkSpec { return n.spec }

func (n *recordingNetwork) StartNode(_ context.Context, id NodeID) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.started = append(n.started, id)
	return nil
}

func (n *recordingNetwork) StopNode(_ context.Context, id NodeID) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.stopped = append(n.stopped, id)
	return nil
}

func (n *recordingNetwork) RestartNode(_ context.Context, id NodeID) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.restarts = append(n.restarts, id)
	return nil
}

func (n *recordingNetwork) Snapshot(ctx context.Context) (Snapshot, error) {
	return snapshot(ctx, n.spec, n.reader)
}

func (n *recordingNetwork) Close(context.Context) error { return nil }

type advancingReader struct {
	mu      sync.Mutex
	heights map[NodeID]int64
}

func newAdvancingReader(spec NetworkSpec) *advancingReader {
	heights := make(map[NodeID]int64, len(spec.Nodes))
	for _, node := range spec.Nodes {
		heights[node.ID] = 1
	}
	return &advancingReader{heights: heights}
}

func (r *advancingReader) GetNodeStatus(_ context.Context, node NodeSpec) (NodeStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.heights[node.ID]++
	return NodeStatus{
		ID:             node.ID,
		Endpoint:       node.Endpoint,
		Reachable:      true,
		Ready:          true,
		Live:           true,
		Height:         r.heights[node.ID],
		ValidatorPower: 10,
		ObservedAt:     time.Now().UTC(),
	}, nil
}

func (f *fakeLifecycleController) JailNode(_ context.Context, node NodeSpec) error {
	f.deregistered = append(f.deregistered, "jail:"+node.ID)
	return nil
}

func (f *fakeLifecycleController) UnjailNode(_ context.Context, node NodeSpec) error {
	f.registered = append(f.registered, "unjail:"+node.ID)
	return nil
}

func TestValidatorChaosScenarioRunsWith300Nodes(t *testing.T) {
	const nodes = 300
	spec := NetworkSpec{Name: "chaos"}
	for i := 0; i < nodes; i++ {
		id := NodeID("node" + itoa(i+1))
		spec.Nodes = append(spec.Nodes, NodeSpec{ID: id, Endpoint: "https://" + string(id) + ".oap.devnet"})
	}

	reader := newAdvancingReader(spec)
	network := &recordingNetwork{spec: spec, reader: reader}
	controller := &fakeLifecycleController{}
	scenario := ValidatorChaosScenario(spec, ValidatorChaosController{
		Registrar:       controller,
		EndpointMutator: controller,
		Jailer:          controller,
	}, ValidatorChaosOptions{
		Seed:                 42,
		Steps:                100,
		StepTimeout:          time.Second,
		LivenessEvery:        20,
		LivenessWithin:       time.Second,
		PollInterval:         time.Millisecond,
		StartNodes:           true,
		IncludeProcessFaults: true,
	})

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(context.Background(), scenario)
	if err != nil {
		t.Fatalf("scenario failed after %d events: %v", len(result.Events), err)
	}
	if len(result.Events) == 0 {
		t.Fatal("expected scenario events")
	}
	network.mu.Lock()
	started := len(network.started)
	network.mu.Unlock()
	if started < nodes {
		t.Fatalf("expected at least %d start calls, got %d", nodes, started)
	}
}

func TestRandomMinorityCohortNeverSelectsMajority(t *testing.T) {
	ids := make([]NodeID, 300)
	for i := range ids {
		ids[i] = NodeID("node" + itoa(i+1))
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 1000; i++ {
		cohort := randomMinorityCohort(rng, ids)
		if len(cohort) == 0 {
			t.Fatal("expected non-empty cohort")
		}
		if len(cohort)*3 > len(ids) {
			t.Fatalf("cohort selected majority-or-larger partition: %d/%d", len(cohort), len(ids))
		}
	}
}

func TestQuorumLossCohortBreaksQuorumAt300Nodes(t *testing.T) {
	ids := make([]NodeID, 300)
	for i := range ids {
		ids[i] = NodeID("node" + itoa(i+1))
	}
	cohort := quorumLossCohort(ids)
	if len(cohort) != 100 {
		t.Fatalf("expected 100 nodes to break quorum out of 300, got %d", len(cohort))
	}
}

func TestQuorumPreservingCohortKeepsQuorumAt300Nodes(t *testing.T) {
	ids := make([]NodeID, 300)
	for i := range ids {
		ids[i] = NodeID("node" + itoa(i+1))
	}
	cohort := quorumPreservingCohort(ids)
	if len(cohort) != 99 {
		t.Fatalf("expected 99 nodes to preserve quorum out of 300, got %d", len(cohort))
	}
}

func TestMinimumQuorumNodes(t *testing.T) {
	tests := map[int]int{
		0:   0,
		1:   1,
		3:   3,
		4:   3,
		300: 201,
	}
	for nodes, want := range tests {
		if got := minimumQuorumNodes(nodes); got != want {
			t.Fatalf("minimumQuorumNodes(%d) = %d, want %d", nodes, got, want)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
