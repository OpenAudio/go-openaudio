package fuzz

import (
	"context"
	"testing"
	"time"
)

func FuzzSimulatedChaosProgram(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 4)
	f.Add([]byte{5}, 1)
	f.Add([]byte{6}, 1)
	f.Add([]byte{8}, 1)
	f.Add([]byte{7, 0, 255, 1, 44, 2, 88}, 300)

	f.Fuzz(func(t *testing.T, program []byte, nodeCount int) {
		network, err := NewSimulatedNetwork(SimulatedNetworkOptions{
			NodeCount:      nodeCount,
			InitialActive:  nodeCount,
			NodePowers:     SeededValidatorPowers(nodeCount, int64(programNodeIndex(program, 0))),
			TickOnSnapshot: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		scenario := SimulatedChaosScenarioFromProgram(network.Spec(), ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, program, SimulatedProgramOptions{
			MaxSteps:                64,
			LivenessEvery:           25,
			LivenessWithin:          25 * time.Millisecond,
			PollInterval:            time.Millisecond,
			AssertAfterEachStep:     true,
			AssertConvergence:       true,
			IncludePersistentFaults: true,
			RecoverAtEnd:            true,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		result, err := Runner{
			Network:     network,
			StepTimeout: time.Second,
		}.Run(ctx, scenario)
		if err != nil {
			t.Fatalf("simulated chaos program failed after %d events: %v", len(result.Events), err)
		}
	})
}
