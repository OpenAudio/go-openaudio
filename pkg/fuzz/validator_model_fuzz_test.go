package fuzz

import "testing"

func FuzzValidatorLifecycleModel(f *testing.F) {
	f.Add([]byte{byte(ModelJail), byte(ModelDeregister)}, 1)
	f.Add([]byte{byte(ModelDeregisterTwice)}, 1)
	f.Add([]byte{byte(ModelStop), byte(ModelTick), byte(ModelStart), byte(ModelTick)}, 4)
	f.Add([]byte{byte(ModelLieEndpoint), byte(ModelRepairEndpoint), byte(ModelTick)}, 300)

	f.Fuzz(func(t *testing.T, program []byte, nodeCount int) {
		_, err := RunValidatorLifecycleProgram(program, nodeCount, ValidatorSetBehaviorCurrent)
		if err != nil {
			t.Fatalf("current validator lifecycle invariant failed: %v", err)
		}
	})
}
