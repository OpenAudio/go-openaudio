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
