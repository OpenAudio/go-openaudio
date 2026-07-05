package fuzz

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestValidatorPowerRestoredRejectsValidatorIdentitySwap(t *testing.T) {
	spec := NetworkSpec{
		Name: "validator-power-identity",
		Nodes: []NodeSpec{
			{ID: "node1", Endpoint: "http://node1"},
			{ID: "node2", Endpoint: "http://node2"},
			{ID: "node3", Endpoint: "http://node3"},
		},
	}
	initial, err := NewStaticNetwork(spec, staticStatusReader{
		"node1": {Reachable: true, Ready: true, Live: true, ValidatorPower: 10, Height: 10},
		"node2": {Reachable: true, Ready: true, Live: true, ValidatorPower: 10, Height: 10},
		"node3": {Reachable: true, Ready: true, Live: false, ValidatorPower: 0, Height: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	swapped, err := NewStaticNetwork(spec, staticStatusReader{
		"node1": {Reachable: true, Ready: true, Live: false, ValidatorPower: 0, Height: 11},
		"node2": {Reachable: true, Ready: true, Live: true, ValidatorPower: 10, Height: 11},
		"node3": {Reachable: true, Ready: true, Live: true, ValidatorPower: 10, Height: 11},
	})
	if err != nil {
		t.Fatal(err)
	}

	baseline := &ValidatorPowerBaseline{}
	ctx := context.Background()
	if err := CaptureValidatorPowerBaseline(baseline).Run(ctx, &RunContext{Network: initial}); err != nil {
		t.Fatal(err)
	}

	err = ValidatorPowerRestored(baseline, 5*time.Millisecond, time.Millisecond).Check(ctx, &RunContext{Network: swapped})
	if err == nil {
		t.Fatal("expected validator identity swap to fail baseline restore")
	}
	if !strings.Contains(err.Error(), "node1") || !strings.Contains(err.Error(), "node3") {
		t.Fatalf("expected mismatch to name swapped validators, got %v", err)
	}
}
