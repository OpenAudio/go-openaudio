package fuzz

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidatorChaosScenarioDefaultsAssertEveryStepOutcome(t *testing.T) {
	spec := generatedChaosTestSpec(4)
	scenario := ValidatorChaosScenario(spec, ValidatorChaosController{}, ValidatorChaosOptions{
		Seed:  7,
		Steps: 3,
	})

	chaosSteps := 0
	for _, step := range scenario.Steps {
		if !strings.HasPrefix(step.Name, "chaos ") {
			continue
		}
		chaosSteps++
		assertionNames := assertionNames(step.Assertions)
		requireAssertion(t, assertionNames, "height follows validator quorum")
		requireAssertion(t, assertionNames, "live validator heights converge")
		requireAssertion(t, assertionNames, "no live validator fork")
		requireAssertion(t, assertionNames, "no height regression")
	}
	if chaosSteps != 3 {
		t.Fatalf("inspected %d chaos steps, want 3", chaosSteps)
	}
}

func TestSimulatedChaosProgramDefaultsCatchOutcomeBeforeLaterRepair(t *testing.T) {
	network := newTransientGeneratedOutcomeNetwork(4)
	program := []byte{
		1, // step 1: persistent action selected
		1, // step 2: persistent action selected
		0,
		2, // step 1 action: deregister
		3, // step 2 action: register
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		SimulatedChaosScenarioFromProgram(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, program, SimulatedProgramOptions{
			MaxSteps:                2,
			LivenessWithin:          25 * time.Millisecond,
			PollInterval:            time.Millisecond,
			IncludePersistentFaults: true,
		}),
	)
	if err == nil {
		t.Fatalf("expected generated chaos to catch stalled outcome after deregister before later repair; events=%d", len(result.Events))
	}
	if !strings.Contains(err.Error(), "expected height to advance with live validator power") {
		t.Fatalf("expected validator quorum height-advance failure, got %v", err)
	}
}

func TestSimulatedChaosProgramCatchesNoopGeneratedBounce(t *testing.T) {
	network := newTransientGeneratedOutcomeNetwork(4)

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		SimulatedChaosScenarioFromProgram(network.Spec(), ValidatorChaosController{}, []byte{0}, SimulatedProgramOptions{
			MaxSteps:       1,
			LivenessWithin: 25 * time.Millisecond,
			PollInterval:   time.Millisecond,
		}),
	)
	if err == nil {
		t.Fatalf("expected generated bounce to catch no-op stop after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "nodes did not become unavailable") {
		t.Fatalf("expected node unavailable failure, got %v", err)
	}
}

func TestSimulatedChaosProgramCatchesNoopGeneratedDeregister(t *testing.T) {
	network := newTransientGeneratedOutcomeNetwork(4)

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		SimulatedChaosScenarioFromProgram(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, []byte{2}, SimulatedProgramOptions{
			MaxSteps:       1,
			LivenessWithin: 25 * time.Millisecond,
			PollInterval:   time.Millisecond,
		}),
	)
	if err == nil {
		t.Fatalf("expected generated deregister to catch unchanged validator power after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "nodes still had validator power") {
		t.Fatalf("expected validator power removal failure, got %v", err)
	}
}

func TestSimulatedChaosProgramCatchesNoopGeneratedJail(t *testing.T) {
	network := newTransientGeneratedOutcomeNetwork(4)

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		SimulatedChaosScenarioFromProgram(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, []byte{4}, SimulatedProgramOptions{
			MaxSteps:       1,
			LivenessWithin: 25 * time.Millisecond,
			PollInterval:   time.Millisecond,
		}),
	)
	if err == nil {
		t.Fatalf("expected generated jail to catch unchanged validator power after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "nodes still had validator power") {
		t.Fatalf("expected validator power removal failure, got %v", err)
	}
}

func TestSimulatedChaosProgramCatchesNoopGeneratedEndpointLie(t *testing.T) {
	network := newTransientGeneratedOutcomeNetwork(4)

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		SimulatedChaosScenarioFromProgram(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, []byte{7}, SimulatedProgramOptions{
			MaxSteps:       1,
			LivenessWithin: 25 * time.Millisecond,
			PollInterval:   time.Millisecond,
		}),
	)
	if err == nil {
		t.Fatalf("expected generated endpoint lie to catch unchanged endpoint health after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "nodes did not become unavailable") {
		t.Fatalf("expected node unavailable failure, got %v", err)
	}
}

func TestSimulatedChaosProgramCatchesNoopGeneratedEndpointRepair(t *testing.T) {
	network := newEndpointRepairNoopGeneratedOutcomeNetwork(4)

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		SimulatedChaosScenarioFromProgram(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, []byte{7}, SimulatedProgramOptions{
			MaxSteps:       1,
			LivenessWithin: 25 * time.Millisecond,
			PollInterval:   time.Millisecond,
		}),
	)
	if err == nil {
		t.Fatalf("expected generated endpoint repair to catch stale endpoint after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "nodes did not become available") {
		t.Fatalf("expected node available failure, got %v", err)
	}
}

func TestOutcomeEdgeCaseScenarioCatchesNoopStop(t *testing.T) {
	network := newTransientGeneratedOutcomeNetwork(4)

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		OutcomeEdgeCaseScenario(network.Spec(), ValidatorChaosController{}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected outcome edge case scenario to catch no-op stop after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "nodes did not become unavailable") {
		t.Fatalf("expected node unavailable failure, got %v", err)
	}
}

func generatedChaosTestSpec(nodes int) NetworkSpec {
	spec := NetworkSpec{Name: "generated-chaos-test"}
	for i := 0; i < nodes; i++ {
		id := NodeID(fmt.Sprintf("node%d", i+1))
		spec.Nodes = append(spec.Nodes, NodeSpec{ID: id, Endpoint: fmt.Sprintf("http://%s", id)})
	}
	return spec
}

func assertionNames(assertions []Assertion) []string {
	names := make([]string, 0, len(assertions))
	for _, assertion := range assertions {
		if assertion == nil {
			continue
		}
		names = append(names, assertion.Name())
	}
	return names
}

func requireAssertion(t *testing.T, names []string, expected string) {
	t.Helper()
	for _, name := range names {
		if name == expected {
			return
		}
	}
	t.Fatalf("missing assertion %q in %v", expected, names)
}

type transientGeneratedOutcomeNetwork struct {
	mu      sync.Mutex
	spec    NetworkSpec
	height  int64
	stalled bool
}

func newTransientGeneratedOutcomeNetwork(nodes int) *transientGeneratedOutcomeNetwork {
	return &transientGeneratedOutcomeNetwork{spec: generatedChaosTestSpec(nodes)}
}

func (n *transientGeneratedOutcomeNetwork) Spec() NetworkSpec {
	return n.spec
}

func (n *transientGeneratedOutcomeNetwork) StartNode(context.Context, NodeID) error {
	return nil
}

func (n *transientGeneratedOutcomeNetwork) StopNode(context.Context, NodeID) error {
	return nil
}

func (n *transientGeneratedOutcomeNetwork) RestartNode(context.Context, NodeID) error {
	return nil
}

func (n *transientGeneratedOutcomeNetwork) DeregisterNode(context.Context, NodeSpec) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.stalled = true
	return nil
}

func (n *transientGeneratedOutcomeNetwork) RegisterNode(context.Context, NodeSpec) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.stalled = false
	return nil
}

func (n *transientGeneratedOutcomeNetwork) JailNode(context.Context, NodeSpec) error {
	return nil
}

func (n *transientGeneratedOutcomeNetwork) UnjailNode(context.Context, NodeSpec) error {
	return nil
}

func (n *transientGeneratedOutcomeNetwork) SetNodeEndpoint(context.Context, NodeSpec, string) error {
	return nil
}

func (n *transientGeneratedOutcomeNetwork) Snapshot(context.Context) (Snapshot, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.stalled {
		n.height++
	}
	statuses := make([]NodeStatus, 0, len(n.spec.Nodes))
	for _, node := range n.spec.Nodes {
		statuses = append(statuses, NodeStatus{
			ID:             node.ID,
			Endpoint:       node.Endpoint,
			Reachable:      true,
			Ready:          true,
			Live:           true,
			Synced:         true,
			Height:         n.height,
			BlockHash:      simulatedBlockHash(n.height),
			ValidatorPower: 10,
			ObservedAt:     time.Now().UTC(),
		})
	}
	return Snapshot{ObservedAt: time.Now().UTC(), Nodes: statuses}, nil
}

func (n *transientGeneratedOutcomeNetwork) Close(context.Context) error {
	return nil
}

type endpointRepairNoopGeneratedOutcomeNetwork struct {
	mu     sync.Mutex
	spec   NetworkSpec
	height int64
	bad    map[NodeID]bool
}

func newEndpointRepairNoopGeneratedOutcomeNetwork(nodes int) *endpointRepairNoopGeneratedOutcomeNetwork {
	return &endpointRepairNoopGeneratedOutcomeNetwork{
		spec: generatedChaosTestSpec(nodes),
		bad:  make(map[NodeID]bool),
	}
}

func (n *endpointRepairNoopGeneratedOutcomeNetwork) Spec() NetworkSpec {
	return n.spec
}

func (n *endpointRepairNoopGeneratedOutcomeNetwork) StartNode(context.Context, NodeID) error {
	return nil
}

func (n *endpointRepairNoopGeneratedOutcomeNetwork) StopNode(context.Context, NodeID) error {
	return nil
}

func (n *endpointRepairNoopGeneratedOutcomeNetwork) RestartNode(context.Context, NodeID) error {
	return nil
}

func (n *endpointRepairNoopGeneratedOutcomeNetwork) SetNodeEndpoint(_ context.Context, node NodeSpec, endpoint string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if endpoint != "" {
		n.bad[node.ID] = true
	}
	return nil
}

func (n *endpointRepairNoopGeneratedOutcomeNetwork) Snapshot(context.Context) (Snapshot, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.height++
	statuses := make([]NodeStatus, 0, len(n.spec.Nodes))
	for _, node := range n.spec.Nodes {
		reachable := !n.bad[node.ID]
		statuses = append(statuses, NodeStatus{
			ID:             node.ID,
			Endpoint:       node.Endpoint,
			Reachable:      reachable,
			Ready:          reachable,
			Live:           true,
			Synced:         true,
			Height:         n.height,
			BlockHash:      simulatedBlockHash(n.height),
			ValidatorPower: 10,
			ObservedAt:     time.Now().UTC(),
		})
	}
	return Snapshot{ObservedAt: time.Now().UTC(), Nodes: statuses}, nil
}

func (n *endpointRepairNoopGeneratedOutcomeNetwork) Close(context.Context) error {
	return nil
}
