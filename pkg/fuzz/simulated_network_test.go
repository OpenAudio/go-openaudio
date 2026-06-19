package fuzz

import (
	"context"
	"strings"
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

func TestSimulatedEndpointLiePreservesConsensusLiveness(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      3,
		InitialActive:  3,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	node, _ := network.Spec().Node("node1")
	if err := network.SetNodeEndpoint(context.Background(), node, "https://wrong-node1.oap.invalid"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := network.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	status, ok := snapshot.ByNode("node1")
	if !ok {
		t.Fatal("node1 missing from snapshot")
	}
	if status.Reachable {
		t.Fatalf("node1 should not be reachable through a bad advertised endpoint: %+v", status)
	}
	if !status.Live || status.ValidatorPower == 0 {
		t.Fatalf("bad endpoint should not remove consensus liveness or validator power: %+v", status)
	}
	if !snapshot.HasValidatorQuorum() {
		t.Fatalf("bad endpoint should not break consensus quorum: %s", snapshot.Summary())
	}
}

func TestSimulatedWeightedPowerAffectsConsensusQuorum(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:     5,
		InitialActive: 5,
		NodePowers: map[NodeID]int64{
			"node1": 40,
			"node2": 15,
			"node3": 15,
			"node4": 15,
			"node5": 15,
		},
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := network.StopNode(ctx, "node1"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := network.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.ReachableCount(); got != 4 {
		t.Fatalf("reachable count after high-power stop = %d, want 4", got)
	}
	totalPower, livePower := snapshot.ValidatorPower()
	if totalPower != 100 || livePower != 60 {
		t.Fatalf("validator power after high-power stop = %d/%d, want 60/100", livePower, totalPower)
	}
	if snapshot.HasValidatorQuorum() {
		t.Fatalf("expected high-power stop to lose quorum despite most nodes being live: %s", snapshot.Summary())
	}
}

func TestSimulatedSnapshotsIncludeConsistentBlockHash(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := network.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	for _, node := range snapshot.Nodes {
		if !node.Live {
			continue
		}
		if node.BlockHash == "" {
			t.Fatalf("live simulated node missing block hash: %+v", node)
		}
		if hash == "" {
			hash = node.BlockHash
		} else if node.BlockHash != hash {
			t.Fatalf("simulated live validators disagree on block hash: %s", snapshot.Summary())
		}
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

func TestSimulatedPersistentChaosScenarioRunsWith300Nodes(t *testing.T) {
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
		Seed:                    199,
		Steps:                   100,
		StepTimeout:             time.Second,
		LivenessEvery:           10,
		LivenessWithin:          25 * time.Millisecond,
		PollInterval:            time.Millisecond,
		IncludeProcessFaults:    true,
		NoProcessFaultDelay:     true,
		AssertAfterEachStep:     true,
		AssertConvergence:       true,
		IncludePersistentFaults: true,
		RecoverAtEnd:            true,
	})
	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(context.Background(), scenario)
	if err != nil {
		t.Fatalf("simulated persistent chaos failed after %d events: %v", len(result.Events), err)
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

func TestSimulatedOutcomeEdgeCasesRunWith300Nodes(t *testing.T) {
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
		OutcomeEdgeCaseScenario(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("outcome edge cases failed after %d events: %v", len(result.Events), err)
	}
}

func TestSimulatedCompoundOutcomeEdgeCasesRunWith300Nodes(t *testing.T) {
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
		CompoundOutcomeEdgeCaseScenario(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("compound outcome edge cases failed after %d events: %v", len(result.Events), err)
	}
}

func TestSimulatedPowerSkewOutcomeEdgeCasesRun(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:     5,
		InitialActive: 5,
		NodePowers: map[NodeID]int64{
			"node1": 40,
			"node2": 15,
			"node3": 15,
			"node4": 15,
			"node5": 15,
		},
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		PowerSkewOutcomeScenario(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, "node1", []NodeID{"node4", "node5"}, 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("power-skew outcome edge cases failed after %d events: %v", len(result.Events), err)
	}
}

func TestSimulatedPowerSkewOutcomeEdgeCasesCatchTickWithoutQuorum(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:     5,
		InitialActive: 5,
		Behavior:      ValidatorSetBehaviorBuggyTickWithoutQuorum,
		NodePowers: map[NodeID]int64{
			"node1": 40,
			"node2": 15,
			"node3": 15,
			"node4": 15,
			"node5": 15,
		},
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		PowerSkewOutcomeScenario(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, "node1", []NodeID{"node4", "node5"}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected power-skew outcome edge cases to catch tick-without-quorum bug after %d events", len(result.Events))
	}
}

func TestQuorumBoundaryPlanUsesObservedValidatorPower(t *testing.T) {
	snapshot := Snapshot{
		Nodes: []NodeStatus{
			{ID: "node1", Live: true, ValidatorPower: 40},
			{ID: "node2", Live: true, ValidatorPower: 15},
			{ID: "node3", Live: true, ValidatorPower: 15},
			{ID: "node4", Live: true, ValidatorPower: 15},
			{ID: "node5", Live: true, ValidatorPower: 15},
		},
	}
	plan, err := quorumBoundaryPlan(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if plan.totalPower != 100 || plan.livePowerBefore != 100 {
		t.Fatalf("planned from power %d/%d, want 100/100", plan.livePowerBefore, plan.totalPower)
	}
	if plan.livePowerAfter != 70 {
		t.Fatalf("preserving partition leaves power %d, want 70", plan.livePowerAfter)
	}
	if plan.livePowerBreakage != 55 {
		t.Fatalf("boundary break leaves power %d, want 55", plan.livePowerBreakage)
	}
	if len(plan.preserve) != 2 {
		t.Fatalf("preserving partition size = %d, want 2", len(plan.preserve))
	}
	if plan.breaker == "" {
		t.Fatal("expected a boundary breaker")
	}
}

func TestSimulatedPowerBoundaryOutcomeEdgeCasesRunWith300Nodes(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      DefaultModelNodeLimit,
		InitialActive:  DefaultModelNodeLimit,
		NodePowers:     SeededValidatorPowers(DefaultModelNodeLimit, 501),
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		PowerBoundaryOutcomeScenario(25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("power-boundary outcome edge cases failed after %d events: %v", len(result.Events), err)
	}
}

func TestSimulatedPowerBoundaryOutcomeEdgeCasesCatchTickWithoutQuorum(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      5,
		InitialActive:  5,
		Behavior:       ValidatorSetBehaviorBuggyTickWithoutQuorum,
		NodePowers:     SeededValidatorPowers(5, 501),
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		PowerBoundaryOutcomeScenario(25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected power-boundary outcome edge cases to catch tick-without-quorum bug after %d events", len(result.Events))
	}
}

func TestSimulatedProgramRecoveryRepairsPersistentFaults(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      5,
		InitialActive:  5,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	program := []byte{
		1, 0, 0, 0, // stop node1
		1, 0, 0, 2, // deregister node1
		1, 0, 0, 4, // jail node1
		1, 0, 0, 6, // advertise bad endpoint for node1
	}
	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		SimulatedChaosScenarioFromProgram(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, program, SimulatedProgramOptions{
			MaxSteps:                len(program),
			LivenessEvery:           len(program),
			LivenessWithin:          25 * time.Millisecond,
			PollInterval:            time.Millisecond,
			AssertAfterEachStep:     true,
			AssertConvergence:       true,
			IncludePersistentFaults: true,
			RecoverAtEnd:            true,
		}),
	)
	if err != nil {
		t.Fatalf("simulated program recovery failed after %d events: %v", len(result.Events), err)
	}

	snapshot, err := network.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.HasValidatorQuorum() {
		t.Fatalf("recovery did not restore validator quorum: %s", snapshot.Summary())
	}
	for _, node := range snapshot.Nodes {
		if !node.Reachable || !node.Live || node.ValidatorPower <= 0 {
			t.Fatalf("recovery left node unhealthy: %s", snapshot.Summary())
		}
	}
}

func TestSimulatedProgramRecoveryCatchesBrokenRegistrationRecovery(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      1,
		InitialActive:  1,
		Behavior:       ValidatorSetBehaviorBuggyRegisterWithoutCometUpdate,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	program := []byte{1, 0, 0, 2}
	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		SimulatedChaosScenarioFromProgram(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, program, SimulatedProgramOptions{
			MaxSteps:                len(program),
			LivenessEvery:           len(program),
			LivenessWithin:          25 * time.Millisecond,
			PollInterval:            time.Millisecond,
			AssertAfterEachStep:     true,
			AssertConvergence:       true,
			IncludePersistentFaults: true,
			RecoverAtEnd:            true,
		}),
	)
	if err == nil {
		t.Fatalf("expected recovery to catch broken registration after %d events", len(result.Events))
	}
}

func TestSimulatedProgramRecoveryCatchesPartialValidatorSetRestore(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		Behavior:       ValidatorSetBehaviorBuggyRegisterNoop,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	program := []byte{1, 0, 0, 2}
	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		SimulatedChaosScenarioFromProgram(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, program, SimulatedProgramOptions{
			MaxSteps:                1,
			LivenessEvery:           1,
			LivenessWithin:          25 * time.Millisecond,
			PollInterval:            time.Millisecond,
			AssertAfterEachStep:     true,
			AssertConvergence:       true,
			IncludePersistentFaults: true,
			RecoverAtEnd:            true,
		}),
	)
	if err == nil {
		t.Fatalf("expected recovery to catch partial validator-set restore after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator-power baseline failure, got %v", err)
	}
}

func TestSimulatedOutcomeEdgeCasesHandleOneNode(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      1,
		InitialActive:  1,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		OutcomeEdgeCaseScenario(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("one-node outcome edge cases failed after %d events: %v", len(result.Events), err)
	}
}

func TestSimulatedCompoundOutcomeEdgeCasesHandleOneNode(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      1,
		InitialActive:  1,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CompoundOutcomeEdgeCaseScenario(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("one-node compound outcome edge cases failed after %d events: %v", len(result.Events), err)
	}
}

func TestSimulatedOutcomeEdgeCasesCatchStallWithQuorum(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      DefaultModelNodeLimit,
		InitialActive:  DefaultModelNodeLimit,
		Behavior:       ValidatorSetBehaviorBuggyStallWithQuorum,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		OutcomeEdgeCaseScenario(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected outcome edge cases to catch stall-with-quorum bug after %d events", len(result.Events))
	}
}

func TestSimulatedCompoundOutcomeEdgeCasesCatchTickWithoutQuorum(t *testing.T) {
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
		CompoundOutcomeEdgeCaseScenario(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected compound outcome edge cases to catch tick-without-quorum bug after %d events", len(result.Events))
	}
}

func TestSimulatedCompoundOutcomeEdgeCasesCatchJailedDeregistrationRegression(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      DefaultModelNodeLimit,
		InitialActive:  DefaultModelNodeLimit,
		Behavior:       ValidatorSetBehaviorBuggyJailedDeregistration,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CompoundOutcomeEdgeCaseScenario(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected compound outcome edge cases to catch jailed-deregistration bug after %d events", len(result.Events))
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
