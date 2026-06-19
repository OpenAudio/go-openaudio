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
