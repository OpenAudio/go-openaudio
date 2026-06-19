package fuzz

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestJailedDeregisterCompatibilityScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		JailedDeregisterCompatibilityScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("jailed-deregister compatibility failed after %d events: %v", len(result.Events), err)
	}
}

func TestJailedDeregisterCompatibilityScenarioCatchesLegacyHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnJailedDeregister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		JailedDeregisterCompatibilityScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy halt to fail jailed-deregister compatibility after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestJailedDeregisterCompatibilityScenarioCatchesLegacyFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnJailedDeregister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		JailedDeregisterCompatibilityScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy fork to fail jailed-deregister compatibility after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestDuplicateDeregisterIdempotencyScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		DuplicateDeregisterIdempotencyScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("duplicate-deregister idempotency failed after %d events: %v", len(result.Events), err)
	}
}

func TestDuplicateDeregisterIdempotencyScenarioCatchesLegacyHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnDuplicateDeregister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		DuplicateDeregisterIdempotencyScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy halt to fail duplicate-deregister idempotency after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestDuplicateDeregisterIdempotencyScenarioCatchesLegacyFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnDuplicateDeregister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		DuplicateDeregisterIdempotencyScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy fork to fail duplicate-deregister idempotency after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestDuplicateJailIdempotencyScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		DuplicateJailIdempotencyScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("duplicate-jail idempotency failed after %d events: %v", len(result.Events), err)
	}
}

func TestDuplicateJailIdempotencyScenarioCatchesLegacyHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnDuplicateJail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		DuplicateJailIdempotencyScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy halt to fail duplicate-jail idempotency after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestDuplicateJailIdempotencyScenarioCatchesLegacyFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnDuplicateJail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		DuplicateJailIdempotencyScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy fork to fail duplicate-jail idempotency after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestEndpointLieConsensusIsolationScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		EndpointLieConsensusIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("endpoint-lie consensus isolation failed after %d events: %v", len(result.Events), err)
	}
}

func TestEndpointLieConsensusIsolationScenarioCatchesLegacyHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnEndpointLie,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		EndpointLieConsensusIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy halt to fail endpoint-lie consensus isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestEndpointLieConsensusIsolationScenarioCatchesLegacyFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnEndpointLie,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		EndpointLieConsensusIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy fork to fail endpoint-lie consensus isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestEndpointRepairIdempotencyScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		EndpointRepairIdempotencyScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("endpoint-repair idempotency failed after %d events: %v", len(result.Events), err)
	}
}

func TestEndpointRepairIdempotencyScenarioCatchesRepairHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnEndpointRepair,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		EndpointRepairIdempotencyScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected endpoint repair halt to fail idempotency after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestEndpointRepairIdempotencyScenarioCatchesRepairFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnEndpointRepair,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		EndpointRepairIdempotencyScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected endpoint repair fork to fail idempotency after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestEndpointRepairIdempotencyScenarioCatchesStaleRepair(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeKeepBadEndpointOnRepair,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		EndpointRepairIdempotencyScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected stale endpoint repair to fail idempotency after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "reachability did not return to baseline") {
		t.Fatalf("expected reachability baseline failure, got %v", err)
	}
}

func TestEndpointRepairIdempotencyScenarioCatchesNoopRepairHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnEndpointNoopRepair,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		EndpointRepairIdempotencyScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected endpoint noop repair halt to fail idempotency after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestEndpointRepairIdempotencyScenarioCatchesNoopRepairFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnEndpointNoopRepair,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		EndpointRepairIdempotencyScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected endpoint noop repair fork to fail idempotency after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestEndpointRegisterRoundTripScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		EndpointRegisterRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("endpoint register round-trip failed after %d events: %v", len(result.Events), err)
	}
}

func TestEndpointRegisterRoundTripScenarioCatchesStaleEndpoint(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeKeepBadEndpointOnRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		EndpointRegisterRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected stale endpoint to fail endpoint register round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "reachability did not return to baseline") {
		t.Fatalf("expected reachability baseline failure, got %v", err)
	}
}

func TestCohortEndpointConsensusIsolationScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CohortEndpointConsensusIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("cohort endpoint consensus isolation failed after %d events: %v", len(result.Events), err)
	}
}

func TestCohortEndpointConsensusIsolationScenarioCatchesEndpointLieHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node3", "node4"},
		Mode:          VersionSkewModeHaltLegacyOnEndpointLie,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CohortEndpointConsensusIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected endpoint lie halt to fail cohort isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestCohortEndpointConsensusIsolationScenarioCatchesEndpointLieFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node3"},
		Mode:          VersionSkewModeForkLegacyOnEndpointLie,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CohortEndpointConsensusIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected endpoint lie fork to fail cohort isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestCohortEndpointConsensusIsolationScenarioCatchesEndpointRepairHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node3", "node4"},
		Mode:          VersionSkewModeHaltLegacyOnEndpointRepair,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CohortEndpointConsensusIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected endpoint repair halt to fail cohort isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestCohortEndpointConsensusIsolationScenarioCatchesEndpointRepairFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node3"},
		Mode:          VersionSkewModeForkLegacyOnEndpointRepair,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CohortEndpointConsensusIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected endpoint repair fork to fail cohort isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestInactiveEndpointIsolationScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		InactiveEndpointIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
			Registrar:       network,
			Jailer:          network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("inactive-endpoint isolation failed after %d events: %v", len(result.Events), err)
	}
}

func TestInactiveEndpointIsolationScenarioCatchesJailedReactivation(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount: 4,
		Mode:      VersionSkewModeReactivateJailedOnInactiveEndpoint,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		InactiveEndpointIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
			Registrar:       network,
			Jailer:          network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected jailed endpoint reactivation to fail inactive-endpoint isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestInactiveEndpointIsolationScenarioCatchesAbsentReactivation(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount: 4,
		Mode:      VersionSkewModeReactivateAbsentOnInactiveEndpoint,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		InactiveEndpointIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
			Registrar:       network,
			Jailer:          network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected absent endpoint reactivation to fail inactive-endpoint isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestInactiveEndpointIsolationScenarioCatchesLegacyHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnInactiveEndpoint,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		InactiveEndpointIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
			Registrar:       network,
			Jailer:          network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected inactive endpoint halt to fail isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestInactiveEndpointIsolationScenarioCatchesLegacyFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnInactiveEndpoint,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		InactiveEndpointIsolationScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
			Registrar:       network,
			Jailer:          network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected inactive endpoint fork to fail isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestJailedEndpointRepairRoundTripScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		JailedEndpointRepairRoundTripScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
			Jailer:          network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("jailed endpoint repair round-trip failed after %d events: %v", len(result.Events), err)
	}
}

func TestJailedEndpointRepairRoundTripScenarioCatchesStaleRepair(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeKeepBadEndpointOnJailedRepair,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		JailedEndpointRepairRoundTripScenario(network.Spec(), ValidatorChaosController{
			EndpointMutator: network,
			Jailer:          network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected stale jailed endpoint repair to fail round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "reachability did not return to baseline") {
		t.Fatalf("expected reachability baseline failure, got %v", err)
	}
}

func TestStopStartRoundTripScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		StopStartRoundTripScenario(network.Spec(), "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("stop-start round-trip failed after %d events: %v", len(result.Events), err)
	}
}

func TestStopStartRoundTripScenarioCatchesNoopStart(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount: 4,
		Mode:      VersionSkewModeNoopOnStart,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		StopStartRoundTripScenario(network.Spec(), "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected noop start to fail stop-start round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestStopStartRoundTripScenarioCatchesLegacyHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnStart,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		StopStartRoundTripScenario(network.Spec(), "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy halt to fail stop-start round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestStopStartRoundTripScenarioCatchesLegacyFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnStart,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		StopStartRoundTripScenario(network.Spec(), "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy fork to fail stop-start round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestInactiveStartIsolationScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		InactiveStartIsolationScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("inactive-start isolation failed after %d events: %v", len(result.Events), err)
	}
}

func TestInactiveStartIsolationScenarioCatchesJailedReactivation(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount: 4,
		Mode:      VersionSkewModeReactivateJailedOnStart,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		InactiveStartIsolationScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected jailed reactivation to fail inactive-start isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestInactiveStartIsolationScenarioCatchesAbsentReactivation(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount: 4,
		Mode:      VersionSkewModeReactivateAbsentOnStart,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		InactiveStartIsolationScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected absent reactivation to fail inactive-start isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestInactiveStartIsolationScenarioCatchesLegacyHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnInactiveStart,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		InactiveStartIsolationScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy halt to fail inactive-start isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestInactiveStartIsolationScenarioCatchesLegacyFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnInactiveStart,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		InactiveStartIsolationScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy fork to fail inactive-start isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestNonJailedUnjailIsolationScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		NonJailedUnjailIsolationScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("non-jailed unjail isolation failed after %d events: %v", len(result.Events), err)
	}
}

func TestNonJailedUnjailIsolationScenarioCatchesActiveHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnActiveUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		NonJailedUnjailIsolationScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected active unjail halt to fail non-jailed unjail isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestNonJailedUnjailIsolationScenarioCatchesActiveFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnActiveUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		NonJailedUnjailIsolationScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected active unjail fork to fail non-jailed unjail isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestNonJailedUnjailIsolationScenarioCatchesAbsentReactivation(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount: 4,
		Mode:      VersionSkewModeReactivateAbsentOnUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		NonJailedUnjailIsolationScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected absent reactivation to fail non-jailed unjail isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestNonJailedUnjailIsolationScenarioCatchesAbsentHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnAbsentUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		NonJailedUnjailIsolationScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected absent unjail halt to fail non-jailed unjail isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestNonJailedUnjailIsolationScenarioCatchesAbsentFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnAbsentUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		NonJailedUnjailIsolationScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected absent unjail fork to fail non-jailed unjail isolation after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestRegisterRoundTripScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		RegisterRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("register round-trip failed after %d events: %v", len(result.Events), err)
	}
}

func TestRegisterRoundTripScenarioCatchesNoopRegister(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		Behavior:       ValidatorSetBehaviorBuggyRegisterNoop,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		RegisterRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected noop register to fail register round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestRegisterRoundTripScenarioCatchesLegacyHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		RegisterRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy halt to fail register round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestRegisterRoundTripScenarioCatchesLegacyFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		RegisterRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy fork to fail register round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestJailedRegisterRoundTripScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		JailedRegisterRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("jailed register round-trip failed after %d events: %v", len(result.Events), err)
	}
}

func TestJailedRegisterRoundTripScenarioCatchesNoopRegister(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeNoopOnJailedRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		JailedRegisterRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected noop jailed register to fail jailed register round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestJailedRegisterRoundTripScenarioCatchesLegacyHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnJailedRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		JailedRegisterRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected jailed register halt to fail jailed register round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestJailedRegisterRoundTripScenarioCatchesLegacyFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnJailedRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		JailedRegisterRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected jailed register fork to fail jailed register round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestRegisterIdempotencyScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		RegisterIdempotencyScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("register idempotency failed after %d events: %v", len(result.Events), err)
	}
}

func TestRegisterIdempotencyScenarioCatchesActiveHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnActiveRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		RegisterIdempotencyScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected active register halt to fail register idempotency after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestRegisterIdempotencyScenarioCatchesActiveFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnActiveRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		RegisterIdempotencyScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected active register fork to fail register idempotency after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestUnjailRoundTripScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		UnjailRoundTripScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("unjail round-trip failed after %d events: %v", len(result.Events), err)
	}
}

func TestUnjailRoundTripScenarioCatchesNoopUnjail(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeNoopOnUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		UnjailRoundTripScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected noop unjail to fail unjail round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestUnjailRoundTripScenarioCatchesLegacyHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1", "node2"},
		Mode:          VersionSkewModeHaltLegacyOnUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		UnjailRoundTripScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy halt to fail unjail round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestUnjailRoundTripScenarioCatchesLegacyFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		UnjailRoundTripScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, "node4", 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy fork to fail unjail round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestCohortLifecycleRoundTripScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CohortLifecycleRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("cohort lifecycle round-trip failed after %d events: %v", len(result.Events), err)
	}
}

func TestCohortLifecycleRoundTripScenarioCatchesRegisterHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node3", "node4"},
		Mode:          VersionSkewModeHaltLegacyOnRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CohortLifecycleRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy halt to fail cohort lifecycle round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestCohortLifecycleRoundTripScenarioCatchesRegisterFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CohortLifecycleRoundTripScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy fork to fail cohort lifecycle round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestCohortLifecycleRoundTripScenarioCatchesNoopUnjail(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeNoopOnUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CohortLifecycleRoundTripScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected noop unjail to fail cohort lifecycle round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestCohortLifecycleRoundTripScenarioCatchesUnjailHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node3", "node4"},
		Mode:          VersionSkewModeHaltLegacyOnUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CohortLifecycleRoundTripScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy halt to fail cohort lifecycle round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "validator power did not return to baseline") {
		t.Fatalf("expected validator power baseline failure, got %v", err)
	}
}

func TestCohortLifecycleRoundTripScenarioCatchesUnjailFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeForkLegacyOnUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		CohortLifecycleRoundTripScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected legacy fork to fail cohort lifecycle round-trip after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestMixedLifecycleQuorumRecoveryScenarioPassesCurrentNetwork(t *testing.T) {
	network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
		NodeCount:      4,
		InitialActive:  4,
		TickOnSnapshot: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		MixedLifecycleQuorumRecoveryScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
			Jailer:    network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("mixed lifecycle quorum recovery failed after %d events: %v", len(result.Events), err)
	}
}

func TestMixedLifecycleQuorumRecoveryScenarioCatchesRegisterHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node3", "node4"},
		Mode:          VersionSkewModeHaltLegacyOnRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		MixedLifecycleQuorumRecoveryScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected register halt to fail mixed lifecycle recovery after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "height did not advance") {
		t.Fatalf("expected height-advance failure, got %v", err)
	}
}

func TestMixedLifecycleQuorumRecoveryScenarioCatchesRegisterFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node3"},
		Mode:          VersionSkewModeForkLegacyOnRegister,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		MixedLifecycleQuorumRecoveryScenario(network.Spec(), ValidatorChaosController{
			Registrar: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected register fork to fail mixed lifecycle recovery after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}

func TestMixedLifecycleQuorumRecoveryScenarioCatchesNoopUnjail(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node1"},
		Mode:          VersionSkewModeNoopOnUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		MixedLifecycleQuorumRecoveryScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected noop unjail to fail mixed lifecycle recovery after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "height did not advance") {
		t.Fatalf("expected height-advance failure, got %v", err)
	}
}

func TestMixedLifecycleQuorumRecoveryScenarioCatchesUnjailHalt(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node3", "node4"},
		Mode:          VersionSkewModeHaltLegacyOnUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		MixedLifecycleQuorumRecoveryScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected unjail halt to fail mixed lifecycle recovery after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "height did not advance") {
		t.Fatalf("expected height-advance failure, got %v", err)
	}
}

func TestMixedLifecycleQuorumRecoveryScenarioCatchesUnjailFork(t *testing.T) {
	network, err := NewVersionSkewNetwork(VersionSkewNetworkOptions{
		NodeCount:     4,
		LegacyNodeIDs: []NodeID{"node3"},
		Mode:          VersionSkewModeForkLegacyOnUnjail,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Runner{Network: network, StepTimeout: time.Second}.Run(
		context.Background(),
		MixedLifecycleQuorumRecoveryScenario(network.Spec(), ValidatorChaosController{
			Jailer: network,
		}, 25*time.Millisecond, time.Millisecond),
	)
	if err == nil {
		t.Fatalf("expected unjail fork to fail mixed lifecycle recovery after %d events", len(result.Events))
	}
	if !strings.Contains(err.Error(), "live validators reported conflicting block hashes") {
		t.Fatalf("expected live-validator fork failure, got %v", err)
	}
}
