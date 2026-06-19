package fuzz

import (
	"context"
	"testing"
	"time"
)

func TestSimulatedNetworkLifecycleAffectsSnapshots(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      3,
		InitialActive:  3,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	before, err := network.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.ReachableCount() != 3 {
		t.Fatalf("reachable before deregister = %d, want 3", before.ReachableCount())
	}

	node, _ := network.Spec().Node("node1")
	if err := network.DeregisterNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	after, err := network.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status, ok := after.ByNode("node1")
	if !ok {
		t.Fatal("node1 missing from snapshot")
	}
	if status.Reachable || status.ValidatorPower != 0 {
		t.Fatalf("node1 should be deregistered: %+v", status)
	}
	if after.MaxHeight() <= before.MaxHeight() {
		t.Fatalf("height did not advance: before=%d after=%d", before.MaxHeight(), after.MaxHeight())
	}
}

func TestSimulatedChaosScenarioRunsWith300Nodes(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      DefaultModelNodeLimit,
		InitialActive:  DefaultModelNodeLimit,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	scenario := ValidatorChaosScenario(network.Spec(), ValidatorChaosController{
		Registrar:       network,
		EndpointMutator: network,
		Jailer:          network,
	}, ValidatorChaosOptions{
		Seed:                 99,
		Steps:                500,
		StepTimeout:          time.Second,
		LivenessEvery:        25,
		LivenessWithin:       time.Second,
		PollInterval:         time.Millisecond,
		IncludeProcessFaults: true,
	})
	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(context.Background(), scenario)
	if err != nil {
		t.Fatalf("simulated chaos failed after %d events: %v", len(result.Events), err)
	}
}

func TestSimulatedQuorumLossRecoveryRunsWith300Nodes(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      DefaultModelNodeLimit,
		InitialActive:  DefaultModelNodeLimit,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		QuorumLossRecoveryScenario(network.Spec(), 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("quorum-loss recovery failed after %d events: %v", len(result.Events), err)
	}
}

func TestSimulatedQuorumLossRecoveryCatchesTickWithoutQuorum(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      DefaultModelNodeLimit,
		InitialActive:  DefaultModelNodeLimit,
		Behavior:       ValidatorSetBehaviorBuggyTickWithoutQuorum,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		QuorumLossRecoveryScenario(network.Spec(), 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected quorum-loss recovery to catch tick-without-quorum bug after %d events", len(result.Events))
	}
}

func TestSimulatedChaosCatchesIncidentRegression(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount: 1,
		Behavior:  ValidatorSetBehaviorBuggyJailedDeregistration,
	})
	if err != nil {
		t.Fatal(err)
	}

	node, _ := network.Spec().Node("node1")
	ctx := context.Background()
	if err := network.JailNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := network.DeregisterNode(ctx, node); !IsModelInvariantError(err) {
		t.Fatalf("expected incident invariant error, got %v", err)
	}
}
